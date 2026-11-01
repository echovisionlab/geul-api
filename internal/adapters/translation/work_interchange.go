package translationadapter

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	"github.com/echovisionlab/geul-api/internal/work"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

// WorkInterchangePort maps Translation application's generic XLIFF command to
// the Work-owned lock, lifecycle-aware CAS and exact locale mutation boundary.
type WorkInterchangePort struct {
	audit        domainaudit.Appender
	auditBuilder LocaleContentAuditBuilder
}

func NewWorkInterchangePort(
	audit domainaudit.Appender,
	auditBuilder LocaleContentAuditBuilder,
) *WorkInterchangePort {
	if audit == nil || auditBuilder == nil {
		panic("Work translation interchange Audit dependencies are required")
	}
	return &WorkInterchangePort{audit: audit, auditBuilder: auditBuilder}
}

func (*WorkInterchangePort) LoadTranslationInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	if entityType != string(core.KindWork) || plan == nil ||
		plan.EntityType != entityType || plan.EntityID != entityID || plan.TargetLocale != locale {
		return application.TranslationInterchangeTargetState{}, errs.InvalidArgument(
			"target", "Work translation interchange identity does not match the route",
		)
	}
	if db == nil || store == nil {
		return application.TranslationInterchangeTargetState{}, errors.New("Work translation interchange load identity is invalid")
	}
	state, err := work.LoadTranslationInterchangeTarget(
		ctx, workStore(db, store), entityID, locale, plan, ProjectRichTextInterchangeTargets,
	)
	if err != nil {
		return application.TranslationInterchangeTargetState{}, err
	}
	return application.TranslationInterchangeTargetState{
		Exists: state.Exists, Revision: state.Revision, Targets: state.Targets,
	}, nil
}

func (p *WorkInterchangePort) ApplyTranslationInterchange(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	command application.TranslationInterchangeApply,
) (application.TranslationInterchangeApplyResult, error) {
	if command.EntityType != string(core.KindWork) || command.Plan == nil || command.Source == nil ||
		command.Plan.EntityType != command.EntityType || command.Plan.EntityID != command.EntityID ||
		command.Plan.SourceLocale != command.SourceLocale || command.Plan.TargetLocale != command.TargetLocale {
		return application.TranslationInterchangeApplyResult{}, errs.InvalidArgument(
			"target", "Work translation interchange identity does not match the validated plan",
		)
	}
	memberID, err := translationInterchangeRequesterMemberID(ctx)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	if db == nil || store == nil {
		return application.TranslationInterchangeApplyResult{}, errors.New("Work translation interchange load identity is invalid")
	}
	result, err := work.ApplyTranslationInterchange(ctx, workStore(db, store), work.TranslationInterchangeMutation{
		WorkID: command.EntityID, SourceLocale: command.SourceLocale, TargetLocale: command.TargetLocale,
		Mode: workInterchangeMode(command.Mode), ExpectedRevision: command.ExpectedRevision,
		Source: command.Source, Plan: command.Plan, Targets: command.Targets,
		UnitHandles: command.UnitHandles, Now: command.Now,
		ProjectTargets: ProjectRichTextInterchangeTargets,
		BuildPatch:     BuildRichTextInterchangePatch,
	})
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	if result.Changed {
		if err := appendLocaleContentInterchangeAudit(
			ctx, db, p.audit, p.auditBuilder, sharedtelemetry.AuditWorkUpdated,
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

var _ application.TranslationInterchangeDomains = (*WorkInterchangePort)(nil)

func workInterchangeMode(mode managev1.TranslationInterchangeMode) work.InterchangeMode {
	switch mode {
	case managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH:
		return work.InterchangePatch
	case managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE:
		return work.InterchangeReplace
	default:
		return 0
	}
}
