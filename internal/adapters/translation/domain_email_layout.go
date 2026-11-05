package translationadapter

import (
	"context"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func emailLayoutDomainRegistration(
	references emailauthoringpostgres.DeliveryReferences,
	audit domainaudit.Appender,
) domainRegistration {
	return newConfiguredDomainPort(core.KindEmailLayout, domainPortFunctions{
		loadSourceDocument: func(
			ctx context.Context,
			db *gorm.DB,
			_ *contentblock.Store,
			entityID string,
		) (*core.SourceDocument, error) {
			locale, found, err := email.ResolveLayoutTranslationSourceLocale(ctx, emailauthoringpostgres.NewRenderingStore(db), entityID)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, fmt.Errorf("email layout %q source locale is not initialized", entityID)
			}
			return email.LoadLayoutTranslationSourceDocument(ctx, emailauthoringpostgres.NewRenderingStore(db), entityID, locale)
		},
		buildExtractionPlan: func(job core.PlanTarget, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return email.BuildLayoutTranslationExtractionPlan(
				job.EntityID, job.SourceLocale, job.TargetLocale, source,
			)
		},
		sourceLocaleAudit: sourceLocaleAudit(sharedtelemetry.AuditEmailLayoutUpdated, sharedtelemetry.NewEmailLayoutSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return emailauthoringpostgres.LayoutTranslationEntrySelectSQL()
		},
		requireInterchangeView: genericInterchangeView(
			core.KindEmailLayout,
			translationCanSet{view: policyv1.EmailLayout.View, edit: policyv1.EmailLayout.Edit},
			"",
		),
		requireSourceLocaleEdit: genericSourceLocaleEdit(
			core.KindEmailLayout,
			translationCanSet{view: policyv1.EmailLayout.View, edit: policyv1.EmailLayout.Edit},
			"",
		),
		requireTranslationRead: requireTranslationEdit(policyv1.EmailLayout.Edit),
		prepareSourceLocale: func(
			ctx context.Context,
			db *gorm.DB,
			entityID string,
			currentSourceLocale string,
			requestedLocale string,
			now time.Time,
		) error {
			return email.PrepareLayoutSourceLocaleSwitch(
				ctx, emailauthoringpostgres.NewRenderingStore(db), entityID, currentSourceLocale, requestedLocale, now,
			)
		},
		requireSourceMutable: func(ctx context.Context, db *gorm.DB, entityID string) error {
			return emailauthoring.RequireTranslationSourceMutable(
				ctx, emailAuthoringStore(db, nil, references), string(core.KindEmailLayout), entityID,
			)
		},
	})
}
