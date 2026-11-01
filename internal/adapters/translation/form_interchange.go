package translationadapter

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	formpostgres "github.com/echovisionlab/geul-api/internal/form/postgres"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// FormInterchange maps Translation application's generic XLIFF command to
// the Form-owned lock, CAS, exact locale write and Audit.
type FormInterchange struct {
	audit domainaudit.Appender
}

// NewFormInterchange returns the Form interchange port; it appends
// locale-content records through audit.
func NewFormInterchange(audit domainaudit.Appender) *FormInterchange {
	if audit == nil {
		panic("Form translation interchange audit is required")
	}
	return &FormInterchange{audit: audit}
}

func (a *FormInterchange) LoadTranslationInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	_ *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	if entityType != string(core.KindForm) || plan == nil || plan.EntityType != entityType ||
		plan.EntityID != entityID || plan.TargetLocale != locale {
		return application.TranslationInterchangeTargetState{}, errs.InvalidArgument(
			"target", "Form translation interchange identity does not match the route",
		)
	}
	state, err := formdomain.LoadTranslationInterchangeTarget(ctx, formStore(db, nil), entityID, locale, plan)
	if err != nil {
		return application.TranslationInterchangeTargetState{}, err
	}
	return application.TranslationInterchangeTargetState{
		Exists: state.Exists, Revision: state.Revision, Targets: state.Targets,
	}, nil
}

func (a *FormInterchange) ApplyTranslationInterchange(
	ctx context.Context,
	db *gorm.DB,
	_ *contentblock.Store,
	command application.TranslationInterchangeApply,
) (application.TranslationInterchangeApplyResult, error) {
	if err := validateScalarInterchangeApply(command, string(core.KindForm)); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	result, err := formdomain.ApplyTranslationInterchange(ctx, formpostgres.Bind(db, formpostgres.Ports{}, a.audit), formdomain.TranslationInterchangeMutation{
		FormID: command.EntityID, SourceLocale: command.SourceLocale, TargetLocale: command.TargetLocale,
		Mode: formInterchangeMode(command.Mode), ExpectedRevision: command.ExpectedRevision,
		Source: command.Source, Plan: command.Plan, Targets: command.Targets,
		UnitHandles: command.UnitHandles, Now: command.Now,
	})
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, mapScalarInterchangeDomainError(err)
	}
	return application.TranslationInterchangeApplyResult{
		Revision: result.Revision, Changed: result.Changed,
		AffectedUnitHandles: result.AffectedUnitHandles,
	}, nil
}

// formInterchangeMode names the Form import mode of mode; an unknown mode
// stays unknown for the Form to reject.
func formInterchangeMode(mode managev1.TranslationInterchangeMode) formdomain.InterchangeMode {
	switch mode {
	case managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH:
		return formdomain.InterchangePatch
	case managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE:
		return formdomain.InterchangeReplace
	default:
		return 0
	}
}

var _ application.TranslationInterchangeDomains = (*FormInterchange)(nil)
