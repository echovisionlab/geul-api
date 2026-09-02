package translationadapter

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	emaildomain "github.com/echovisionlab/geul-api/internal/emailauthoring"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// EmailLayoutInterchange adapts stable Layout unit handles to XLIFF while the
// owning domain retains source wrapper, sparse target and lifecycle authority.
type EmailLayoutInterchange struct {
	references   emaildomain.CampaignDeliveryReferences
	auditWriter  domainaudit.Appender
	auditBuilder LocaleContentAuditBuilder
}

func NewEmailLayoutInterchange(
	references emaildomain.CampaignDeliveryReferences,
	auditWriter domainaudit.Appender,
	auditBuilder LocaleContentAuditBuilder,
) *EmailLayoutInterchange {
	if references == nil || auditWriter == nil || auditBuilder == nil {
		panic("Email Layout translation interchange dependencies are required")
	}
	return &EmailLayoutInterchange{
		references: references, auditWriter: auditWriter, auditBuilder: auditBuilder,
	}
}

func (*EmailLayoutInterchange) LoadTranslationInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	_ *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	if err := validateEmailLayoutInterchangeIdentity(db, entityType, entityID, locale, plan); err != nil {
		return application.TranslationInterchangeTargetState{}, err
	}
	target, err := emaildomain.LoadEmailLayoutTranslationInterchangeTarget(ctx, db, entityID, locale)
	if err != nil {
		return application.TranslationInterchangeTargetState{}, err
	}
	return projectEmailLayoutInterchangeTargets(plan, target), nil
}

func (a *EmailLayoutInterchange) ApplyTranslationInterchange(
	ctx context.Context,
	db *gorm.DB,
	_ *contentblock.Store,
	command application.TranslationInterchangeApply,
) (application.TranslationInterchangeApplyResult, error) {
	if err := validateEmailLayoutInterchangeApply(db, command); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	current, err := emaildomain.LoadEmailLayoutTranslationInterchangeTarget(
		ctx, db, command.EntityID, command.TargetLocale,
	)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	currentState := projectEmailLayoutInterchangeTargets(command.Plan, current)
	if err := requireTranslationInterchangeRevision(currentState, command.ExpectedRevision); err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	targets := interchangeCandidateTargets(command.Mode, currentState.Targets, command.Targets)
	values := make(map[string]string, len(targets))
	for _, unit := range command.Plan.Units {
		if value, ok := targets[unit.UnitID]; ok {
			values[unit.UnitID] = value.TranslatedText
		}
	}
	memberID, err := translationInterchangeRequesterMemberID(ctx)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	expectedRevision := ""
	if command.ExpectedRevision != nil {
		expectedRevision = *command.ExpectedRevision
	}
	after, changed, err := emaildomain.ApplyEmailLayoutTranslationInterchange(
		ctx,
		db,
		a.references,
		command.EntityID,
		command.SourceLocale,
		emaildomain.EmailLayoutTranslationInterchangeMutation{
			TargetLocale: command.TargetLocale, ExpectedRevision: expectedRevision,
			ExpectedPresence: current.Exists, Values: values, Now: command.Now,
		},
	)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	if changed {
		if err := appendLocaleContentInterchangeAudit(
			ctx, db, a.auditWriter, a.auditBuilder, sharedtelemetry.AuditEmailLayoutUpdated,
			memberID, command.EntityID, command.TargetLocale, current.Exists,
		); err != nil {
			return application.TranslationInterchangeApplyResult{}, err
		}
	}
	return application.TranslationInterchangeApplyResult{
		Revision: after.Revision, Changed: changed,
		AffectedUnitHandles: append([]string(nil), command.UnitHandles...),
	}, nil
}

func validateEmailLayoutInterchangeIdentity(
	db *gorm.DB,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) error {
	if db == nil || plan == nil {
		return errors.New("email Layout translation interchange dependencies are required")
	}
	if entityType != string(core.KindEmailLayout) || plan.EntityType != entityType ||
		plan.EntityID != entityID || plan.TargetLocale != locale {
		return errs.InvalidArgument("target", "Email Layout translation interchange identity does not match the extraction plan")
	}
	return nil
}

func validateEmailLayoutInterchangeApply(
	db *gorm.DB,
	command application.TranslationInterchangeApply,
) error {
	if err := validateEmailLayoutInterchangeIdentity(
		db, command.EntityType, command.EntityID, command.TargetLocale, command.Plan,
	); err != nil {
		return err
	}
	if command.Source == nil || command.Source.ContentHTML == nil ||
		command.Plan.SourceLocale != command.SourceLocale || command.Plan.TargetLocale != command.TargetLocale {
		return errs.InvalidArgument("target", "Email Layout translation interchange source does not match the extraction plan")
	}
	if command.Mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH &&
		command.Mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
		return errs.InvalidArgument("mode", "PATCH or REPLACE is required")
	}
	return nil
}

func projectEmailLayoutInterchangeTargets(
	plan *core.ExtractionPlan,
	target emaildomain.EmailLayoutTranslationInterchangeTarget,
) application.TranslationInterchangeTargetState {
	targets := make(map[string]core.UnitResult)
	if target.Exists {
		for _, unit := range plan.Units {
			value, exists := target.Values[unit.UnitID]
			if !exists {
				continue
			}
			targets[unit.UnitID] = core.UnitResult{
				UnitID: unit.UnitID, TranslatedText: value,
			}
		}
	}
	return application.TranslationInterchangeTargetState{
		Exists: target.Exists, Revision: strings.TrimSpace(target.Revision), Targets: targets,
	}
}

var _ application.TranslationInterchangeDomains = (*EmailLayoutInterchange)(nil)
