package translationadapter

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
)

type formTranslationInterchangeDomain interface {
	LoadTranslationInterchangeTarget(
		context.Context, *gorm.DB, string, string, *core.ExtractionPlan,
	) (formdomain.TranslationInterchangeTarget, error)
	ApplyTranslationInterchange(
		context.Context, *gorm.DB, formdomain.TranslationInterchangeMutation,
	) (formdomain.TranslationInterchangeResult, error)
}

type FormInterchange struct {
	domain formTranslationInterchangeDomain
}

func NewFormInterchange(domain formTranslationInterchangeDomain) *FormInterchange {
	if domain == nil {
		panic("Form translation interchange domain is required")
	}
	return &FormInterchange{domain: domain}
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
	state, err := a.domain.LoadTranslationInterchangeTarget(ctx, db, entityID, locale, plan)
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
	result, err := a.domain.ApplyTranslationInterchange(ctx, db, formdomain.TranslationInterchangeMutation{
		FormID: command.EntityID, SourceLocale: command.SourceLocale, TargetLocale: command.TargetLocale,
		Mode: command.Mode, ExpectedRevision: command.ExpectedRevision,
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

var _ application.TranslationInterchangeDomains = (*FormInterchange)(nil)
