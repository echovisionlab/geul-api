package translationadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/page"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

// PageInterchangePort maps Translation application's generic XLIFF command to
// the Page-owned lock, CAS and exact locale mutation boundary.
type PageInterchangePort struct {
	audit        domainaudit.Appender
	auditBuilder LocaleContentAuditBuilder
}

func NewPageInterchangePort(
	audit domainaudit.Appender,
	auditBuilder LocaleContentAuditBuilder,
) *PageInterchangePort {
	if audit == nil || auditBuilder == nil {
		panic("Page translation interchange Audit dependencies are required")
	}
	return &PageInterchangePort{audit: audit, auditBuilder: auditBuilder}
}

func (*PageInterchangePort) LoadTranslationInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	if entityType != string(core.KindPage) || plan == nil ||
		plan.EntityType != entityType || plan.EntityID != entityID || plan.TargetLocale != locale {
		return application.TranslationInterchangeTargetState{}, errs.InvalidArgument(
			"target", "Page translation interchange identity does not match the route",
		)
	}
	state, err := page.LoadTranslationInterchangeTarget(
		ctx, pageStore(db, store), entityID, locale, plan,
		ProjectRichTextInterchangeTargets, InterchangeProtoPathPresent,
		CloneInterchangeUnitResult, EmptyInterchangeTargetInline,
	)
	if err != nil {
		return application.TranslationInterchangeTargetState{}, err
	}
	return application.TranslationInterchangeTargetState{
		Exists: state.Exists, Revision: state.Revision, Targets: state.Targets,
	}, nil
}

func (p *PageInterchangePort) ApplyTranslationInterchange(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	command application.TranslationInterchangeApply,
) (application.TranslationInterchangeApplyResult, error) {
	if command.EntityType != string(core.KindPage) || command.Plan == nil || command.Source == nil ||
		command.Plan.EntityType != command.EntityType || command.Plan.EntityID != command.EntityID ||
		command.Plan.SourceLocale != command.SourceLocale || command.Plan.TargetLocale != command.TargetLocale {
		return application.TranslationInterchangeApplyResult{}, errs.InvalidArgument(
			"target", "Page translation interchange identity does not match the validated plan",
		)
	}
	memberID, err := translationInterchangeRequesterMemberID(ctx)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	result, err := page.ApplyTranslationInterchange(ctx, pageStore(db, store), page.TranslationInterchangeMutation{
		PageID: command.EntityID, SourceLocale: command.SourceLocale, TargetLocale: command.TargetLocale,
		Mode: pageInterchangeMode(command.Mode), ExpectedRevision: command.ExpectedRevision,
		Source: command.Source, Plan: command.Plan, Targets: command.Targets,
		UnitHandles: command.UnitHandles, Now: command.Now,
		ProjectTargets: ProjectRichTextInterchangeTargets,
		BuildPatch:     BuildRichTextInterchangePatch,
		PathPresent:    InterchangeProtoPathPresent,
		CloneResult:    CloneInterchangeUnitResult,
		EmptyInline:    EmptyInterchangeTargetInline,
		CopyPath:       CopyInterchangeProtoPath,
	})
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	if result.Changed {
		if err := appendLocaleContentInterchangeAudit(
			ctx, db, p.audit, p.auditBuilder, sharedtelemetry.AuditPageUpdated,
			memberID, command.EntityID, command.TargetLocale, result.TargetPreviouslyExists,
		); err != nil {
			return application.TranslationInterchangeApplyResult{}, err
		}
	}
	return application.TranslationInterchangeApplyResult{
		Revision: result.Revision, Changed: result.Changed,
		AffectedUnitHandles: result.AffectedUnitHandles,
	}, nil
}

// pageInterchangeMode names the Page import mode of mode; an unknown mode
// stays unknown for the Page to reject.
func pageInterchangeMode(mode managev1.TranslationInterchangeMode) page.InterchangeMode {
	switch mode {
	case managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH:
		return page.InterchangePatch
	case managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE:
		return page.InterchangeReplace
	default:
		return 0
	}
}

var _ application.TranslationInterchangeDomains = (*PageInterchangePort)(nil)
