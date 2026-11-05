package translationadapter

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/form"
	formpostgres "github.com/echovisionlab/geul-api/internal/form/postgres"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func formDomainRegistration(audit domainaudit.Appender) domainRegistration {
	permissions := translationCanSet{
		view: policyv1.Form.View,
		edit: policyv1.Form.Edit,
	}
	return newConfiguredDomainPort(core.KindForm, domainPortFunctions{
		loadSourceDocument: func(ctx context.Context, db *gorm.DB, _ *contentblock.Store, entityID string) (*core.SourceDocument, error) {
			return form.LoadTranslationSourceDocument(ctx, formStore(db, nil), entityID)
		},
		buildExtractionPlan: func(job core.PlanTarget, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return form.BuildTranslationExtractionPlan(job.EntityID, job.SourceLocale, job.TargetLocale, source)
		},
		sourceLocaleAudit: sourceLocaleAudit(sharedtelemetry.AuditFormUpdated, sharedtelemetry.NewFormSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(string) string {
			return formpostgres.TranslationEntrySelectSQL()
		},
		requestLocaleOG: localeAwareOG(core.KindForm),
		requireInterchangeView: genericInterchangeView(
			core.KindForm,
			permissions,
			"",
		),
		requireInterchangeEdit: genericSourceLocaleEdit(
			core.KindForm,
			permissions,
			"",
		),
		requireTranslationRead: requireTranslationEdit(policyv1.Form.Edit),
		requireSourceLocaleEdit: genericSourceLocaleEdit(
			core.KindForm,
			permissions,
			"",
		),
		prepareSourceLocale: func(ctx context.Context, db *gorm.DB, entityID string, currentSourceLocale string, requestedLocale string, now time.Time) error {
			if db == nil {
				return form.PrepareSourceLocaleSwitch(ctx, nil, entityID, currentSourceLocale, requestedLocale, now)
			}
			return form.PrepareSourceLocaleSwitch(ctx, formStore(db, nil), entityID, currentSourceLocale, requestedLocale, now)
		},
	})
}

// formStore binds the Form Store to the translation domain's database
// handle, with the content store it hands the delivery.
func formStore(db *gorm.DB, contents *contentblock.Store) form.Store {
	return formpostgres.NewStore(db, formpostgres.Ports{ContentBlocks: contents})
}
