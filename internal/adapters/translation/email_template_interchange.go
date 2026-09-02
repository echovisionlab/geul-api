package translationadapter

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	emaildomain "github.com/echovisionlab/geul-api/internal/emailauthoring"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// EmailTemplateInterchange adapts the shared Rich Text XLIFF codec to Email
// Template's lifecycle, Content Document CAS, projection and Audit seams.
type EmailTemplateInterchange struct {
	references   emaildomain.CampaignDeliveryReferences
	auditWriter  domainaudit.Appender
	auditBuilder LocaleContentAuditBuilder
}

func NewEmailTemplateInterchange(
	references emaildomain.CampaignDeliveryReferences,
	auditWriter domainaudit.Appender,
	auditBuilder LocaleContentAuditBuilder,
) *EmailTemplateInterchange {
	if references == nil || auditWriter == nil || auditBuilder == nil {
		panic("Email Template translation interchange dependencies are required")
	}
	return &EmailTemplateInterchange{
		references: references, auditWriter: auditWriter, auditBuilder: auditBuilder,
	}
}

type emailRichTextInterchangeTarget struct {
	state     application.TranslationInterchangeTargetState
	localized *contentv1.LocalizedRichTextDocument
}

func (*EmailTemplateInterchange) LoadTranslationInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	loaded, err := loadEmailTemplateInterchangeTarget(
		ctx, db, store, entityType, entityID, locale, plan,
	)
	return loaded.state, err
}

func (a *EmailTemplateInterchange) ApplyTranslationInterchange(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	command application.TranslationInterchangeApply,
) (application.TranslationInterchangeApplyResult, error) {
	if err := validateBlockInterchangeApply(command, string(core.KindEmailTemplate)); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	current, err := loadEmailTemplateInterchangeTarget(
		ctx, db, store, command.EntityType, command.EntityID, command.TargetLocale, command.Plan,
	)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	if err := requireTranslationInterchangeRevision(current.state, command.ExpectedRevision); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	candidate, err := buildEmailRichTextInterchangeCandidate(command, current)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	memberID, err := translationInterchangeRequesterMemberID(ctx)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	result, err := emaildomain.ApplyTemplateTranslationInterchange(
		ctx,
		db,
		store,
		a.references,
		command.EntityID,
		command.SourceLocale,
		emaildomain.TemplateTranslationInterchangeMutation{
			TargetLocale: command.TargetLocale, ExpectedDocumentRevision: command.Source.ContentDocumentRevision,
			ExpectedTargetRevision: command.ExpectedRevision,
			ExpectedPresence:       current.state.Exists, Subject: candidate.Title,
			LocaleMutations:     candidate.RichTextLocaleMutations(),
			ContributorMemberID: memberID, Now: command.Now,
		},
	)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	if result.Changed {
		if err := appendLocaleContentInterchangeAudit(
			ctx, db, a.auditWriter, a.auditBuilder, sharedtelemetry.AuditEmailTemplateUpdated,
			memberID, command.EntityID, command.TargetLocale, current.state.Exists,
		); err != nil {
			return application.TranslationInterchangeApplyResult{}, err
		}
	}
	return application.TranslationInterchangeApplyResult{
		Revision: result.Revision, Changed: result.Changed,
		AffectedUnitHandles: append([]string(nil), command.UnitHandles...),
	}, nil
}

func loadEmailTemplateInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (emailRichTextInterchangeTarget, error) {
	if err := validateCreativeInterchangeLoad(
		db, store, entityType, entityID, locale, plan, string(core.KindEmailTemplate),
	); err != nil {
		return emailRichTextInterchangeTarget{}, err
	}
	target, err := emaildomain.LoadTemplateTranslationInterchangeTarget(
		ctx, db, store, entityID, locale,
	)
	if err != nil {
		return emailRichTextInterchangeTarget{}, err
	}
	return projectEmailRichTextInterchangeTarget(
		plan, target.Exists, target.Revision, target.Subject, target.Document,
	)
}

func projectEmailRichTextInterchangeTarget(
	plan *core.ExtractionPlan,
	exists bool,
	revision string,
	metadata *string,
	document *contentv1.LocalizedRichTextDocument,
) (emailRichTextInterchangeTarget, error) {
	if document == nil {
		return emailRichTextInterchangeTarget{}, errors.New("email Rich Text translation interchange document is required")
	}
	if document.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL {
		return emailRichTextInterchangeTarget{}, errs.FailedPrecondition("Email Rich Text translation interchange requires the Email content profile")
	}
	if !exists && len(document.GetLocaleOverlay().GetBlocks()) != 0 {
		return emailRichTextInterchangeTarget{}, errs.InternalMsg("Email Rich Text target Blocks exist without owning locale metadata")
	}
	targets := make(map[string]core.UnitResult)
	var err error
	if exists {
		targets, err = ProjectRichTextInterchangeTargets(plan, document)
		if err != nil {
			return emailRichTextInterchangeTarget{}, err
		}
		addEntityInterchangeTarget(targets, plan, "title", metadata)
	}
	state := application.TranslationInterchangeTargetState{Exists: exists, Targets: targets}
	if exists {
		state.Revision = revision
	}
	return emailRichTextInterchangeTarget{state: state, localized: document}, nil
}

func buildEmailRichTextInterchangeCandidate(
	command application.TranslationInterchangeApply,
	current emailRichTextInterchangeTarget,
) (*core.Candidate, error) {
	targets := interchangeCandidateTargets(command.Mode, current.state.Targets, command.Targets)
	candidate, err := buildCreativeInterchangeCandidate(command, current.localized)
	if err != nil {
		return nil, err
	}
	candidate.Title = entityInterchangeTarget(targets, command.Plan, "title")
	return candidate, nil
}

var _ application.TranslationInterchangeDomains = (*EmailTemplateInterchange)(nil)
