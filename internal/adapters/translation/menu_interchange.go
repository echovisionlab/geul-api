package translationadapter

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	menudomain "github.com/echovisionlab/geul-api/internal/menu"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
)

type menuTranslationInterchangeDomain interface {
	LoadTranslationInterchangeTarget(
		context.Context, *gorm.DB, string, string, *core.ExtractionPlan,
	) (menudomain.TranslationInterchangeTarget, error)
	ApplyTranslationInterchange(
		context.Context, *gorm.DB, menudomain.TranslationInterchangeMutation,
	) (menudomain.TranslationInterchangeResult, error)
}

type MenuInterchange struct {
	domain menuTranslationInterchangeDomain
}

func NewMenuInterchange(domain menuTranslationInterchangeDomain) *MenuInterchange {
	if domain == nil {
		panic("Menu translation interchange domain is required")
	}
	return &MenuInterchange{domain: domain}
}

func (a *MenuInterchange) LoadTranslationInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	_ *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	if entityType != string(core.KindMenu) || plan == nil || plan.EntityType != entityType ||
		plan.EntityID != entityID || plan.TargetLocale != locale {
		return application.TranslationInterchangeTargetState{}, errs.InvalidArgument(
			"target", "Menu translation interchange identity does not match the route",
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

func (a *MenuInterchange) ApplyTranslationInterchange(
	ctx context.Context,
	db *gorm.DB,
	_ *contentblock.Store,
	command application.TranslationInterchangeApply,
) (application.TranslationInterchangeApplyResult, error) {
	if err := validateScalarInterchangeApply(command, string(core.KindMenu)); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	result, err := a.domain.ApplyTranslationInterchange(ctx, db, menudomain.TranslationInterchangeMutation{
		MenuID: command.EntityID, SourceLocale: command.SourceLocale, TargetLocale: command.TargetLocale,
		Mode: command.Mode, ExpectedRevision: command.ExpectedRevision,
		Plan: command.Plan, Targets: command.Targets,
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

var _ application.TranslationInterchangeDomains = (*MenuInterchange)(nil)
