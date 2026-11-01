package translationadapter

import (
	"context"

	"gorm.io/gorm"

	menuadapter "github.com/echovisionlab/geul-api/internal/adapters/menu"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	menudomain "github.com/echovisionlab/geul-api/internal/menu"
	menupostgres "github.com/echovisionlab/geul-api/internal/menu/postgres"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	translationpostgres "github.com/echovisionlab/geul-api/internal/translation/postgres"
)

// MenuInterchange joins the Menu XLIFF steps to the translation pipeline's
// transaction.
type MenuInterchange struct {
	audit domainaudit.Appender
}

func NewMenuInterchange(audit domainaudit.Appender) *MenuInterchange {
	if audit == nil {
		panic("Menu translation interchange audit is required")
	}
	return &MenuInterchange{audit: audit}
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
	state, err := menudomain.LoadTranslationInterchangeTarget(ctx, menupostgres.NewStore(db, menupostgres.Ports{}), entityID, locale, plan)
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
	result, err := menudomain.ApplyTranslationInterchange(ctx, menuadapter.Join(db, a.audit), menudomain.TranslationInterchangeMutation{
		MenuID: command.EntityID, SourceLocale: command.SourceLocale, TargetLocale: command.TargetLocale,
		Mode: menuInterchangeMode(command.Mode), ExpectedRevision: command.ExpectedRevision,
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

// menuInterchangeMode leaves a mode outside PATCH and REPLACE zero, which
// the Menu rejects.
func menuInterchangeMode(mode application.InterchangeMode) menudomain.InterchangeMode {
	switch mode {
	case application.InterchangeModePatch:
		return menudomain.InterchangePatch
	case application.InterchangeModeReplace:
		return menudomain.InterchangeReplace
	default:
		return 0
	}
}

var _ translationpostgres.InterchangeRegistry = (*MenuInterchange)(nil)
