package translationadapter

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	seriesdomain "github.com/echovisionlab/geul-api/internal/series"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
)

type postSeriesTranslationInterchangeDomain interface {
	LoadTranslationInterchangeTarget(
		context.Context, *gorm.DB, string, string, *core.ExtractionPlan,
	) (seriesdomain.TranslationInterchangeTarget, error)
	ApplyTranslationInterchange(
		context.Context, *gorm.DB, seriesdomain.TranslationInterchangeMutation,
	) (seriesdomain.TranslationInterchangeResult, error)
}

type PostSeriesInterchange struct {
	domain postSeriesTranslationInterchangeDomain
}

func NewPostSeriesInterchange(domain postSeriesTranslationInterchangeDomain) *PostSeriesInterchange {
	if domain == nil {
		panic("Post Series translation interchange domain is required")
	}
	return &PostSeriesInterchange{domain: domain}
}

func (a *PostSeriesInterchange) LoadTranslationInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	_ *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	if entityType != string(core.KindPostSeries) || plan == nil || plan.EntityType != entityType ||
		plan.EntityID != entityID || plan.TargetLocale != locale {
		return application.TranslationInterchangeTargetState{}, errs.InvalidArgument(
			"target", "Post Series translation interchange identity does not match the route",
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

func (a *PostSeriesInterchange) ApplyTranslationInterchange(
	ctx context.Context,
	db *gorm.DB,
	_ *contentblock.Store,
	command application.TranslationInterchangeApply,
) (application.TranslationInterchangeApplyResult, error) {
	if err := validateScalarInterchangeApply(command, string(core.KindPostSeries)); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	result, err := a.domain.ApplyTranslationInterchange(ctx, db, seriesdomain.TranslationInterchangeMutation{
		SeriesID: command.EntityID, SourceLocale: command.SourceLocale, TargetLocale: command.TargetLocale,
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

var _ application.TranslationInterchangeDomains = (*PostSeriesInterchange)(nil)
