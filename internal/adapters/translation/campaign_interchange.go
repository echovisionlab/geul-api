package translationadapter

import (
	"context"

	"gorm.io/gorm"

	campaigndomain "github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// CampaignInterchange adapts the shared Rich Text XLIFF codec to Campaign's
// draft lifecycle, Content Document CAS, projection and Audit seams.
type CampaignInterchange struct {
	auditWriter  domainaudit.Appender
	auditBuilder LocaleContentAuditBuilder
}

func NewCampaignInterchange(
	auditWriter domainaudit.Appender,
	auditBuilder LocaleContentAuditBuilder,
) *CampaignInterchange {
	if auditWriter == nil || auditBuilder == nil {
		panic("Campaign translation interchange Audit dependencies are required")
	}
	return &CampaignInterchange{auditWriter: auditWriter, auditBuilder: auditBuilder}
}

func (*CampaignInterchange) LoadTranslationInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	loaded, err := loadCampaignInterchangeTarget(
		ctx, db, store, entityType, entityID, locale, plan,
	)
	return loaded.state, err
}

func (a *CampaignInterchange) ApplyTranslationInterchange(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	command application.TranslationInterchangeApply,
) (application.TranslationInterchangeApplyResult, error) {
	if err := validateBlockInterchangeApply(command, string(core.KindCampaign)); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	current, err := loadCampaignInterchangeTarget(
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
	result, err := campaigndomain.ApplyTranslationInterchange(
		ctx,
		db,
		store,
		command.EntityID,
		command.SourceLocale,
		campaigndomain.TranslationInterchangeMutation{
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
			ctx, db, a.auditWriter, a.auditBuilder, sharedtelemetry.AuditCampaignUpdated,
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

func loadCampaignInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (emailRichTextInterchangeTarget, error) {
	if err := validateCreativeInterchangeLoad(
		db, store, entityType, entityID, locale, plan, string(core.KindCampaign),
	); err != nil {
		return emailRichTextInterchangeTarget{}, err
	}
	target, err := campaigndomain.LoadTranslationInterchangeTarget(
		ctx, db, store, entityID, locale,
	)
	if err != nil {
		return emailRichTextInterchangeTarget{}, err
	}
	return projectEmailRichTextInterchangeTarget(
		plan, target.Exists, target.Revision, target.Subject, target.Document,
	)
}

var _ application.TranslationInterchangeDomains = (*CampaignInterchange)(nil)
