package translationadapter

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/page"
	pagepostgres "github.com/echovisionlab/geul-api/internal/page/postgres"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func pageDomainRegistration(audit domainaudit.Appender) domainRegistration {
	return newConfiguredDomainPort(core.KindPage, domainPortFunctions{
		loadSourceDocument: func(ctx context.Context, db *gorm.DB, store *contentblock.Store, pageID string) (*core.SourceDocument, error) {
			return page.LoadTranslationSourceDocument(ctx, pageStore(db, store), pageID)
		},
		buildExtractionPlan: func(job core.PlanTarget, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return page.BuildTranslationExtractionPlan(pageTranslationJob(job), source)
		},
		requestLocaleOG:   localeAwareOG(core.KindPage),
		sourceLocaleAudit: sourceLocaleAudit(sharedtelemetry.AuditPageUpdated, sharedtelemetry.NewPageSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, title, summary, NULL::text AS content_html, NULL::text AS content_text, NULL::jsonb AS content_json, updated_at, og_asset_id FROM %s`, table)
		},
		requireInterchangeView: genericInterchangeView(
			core.KindPage,
			translationCanSet{view: policyv1.Page.View, edit: policyv1.Page.Edit},
			"",
		),
		requireSourceLocaleEdit: genericSourceLocaleEdit(
			core.KindPage,
			translationCanSet{view: policyv1.Page.View, edit: policyv1.Page.Edit},
			"",
		),
		requireTranslationRead: requireTranslationEdit(policyv1.Page.Edit),
	})
}

// pageStore binds the Page Store to the translation domain's database
// handle, with the content store it hands the delivery.
func pageStore(db *gorm.DB, contents *contentblock.Store) page.Store {
	return pagepostgres.NewStore(db, pagepostgres.Ports{ContentBlocks: contents})
}

func pageTranslationJob(job core.PlanTarget) page.TranslationJob {
	return page.TranslationJob{
		EntityType: job.EntityType, EntityID: job.EntityID, SourceLocale: job.SourceLocale,
		TargetLocale: job.TargetLocale,
	}
}
