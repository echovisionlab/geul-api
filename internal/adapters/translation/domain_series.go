package translationadapter

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/series"
	seriespostgres "github.com/echovisionlab/geul-api/internal/series/postgres"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func seriesDomainRegistration(audit domainaudit.Appender) domainRegistration {
	return newConfiguredDomainPort(core.KindPostSeries, domainPortFunctions{
		loadSourceDocument: func(ctx context.Context, db *gorm.DB, _ *contentblock.Store, entityID string) (*core.SourceDocument, error) {
			return series.LoadTranslationSourceDocument(ctx, seriespostgres.NewTranslationStore(db), entityID)
		},
		buildExtractionPlan: func(job core.PlanTarget, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return series.BuildTranslationExtractionPlan(job.EntityID, job.SourceLocale, job.TargetLocale, source)
		},
		requestLocaleOG: localeAwareOG(core.KindPostSeries),
		sourceLocaleAudit: sourceLocaleAudit(
			sharedtelemetry.AuditPostSeriesUpdated, sharedtelemetry.NewPostSeriesSourceLocaleAuditRecord,
		),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, title, summary, content_html, content_text, content_json, updated_at, og_asset_id FROM %s`, table)
		},
		requireInterchangeView:  requireSeriesView,
		requireSourceLocaleEdit: requireSeriesEdit,
		requireTranslationRead: func(ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
			err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				return requireSeriesView(ctx, tx, spiceDB, entityID)
			})
			return maskTranslationAuthorizationDenial(err, string(core.KindPostSeries), entityID)
		},
	})
}

func requireSeriesView(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, seriesID string) error {
	return series.RequireViewAndLock(ctx, seriespostgres.NewTranslationStore(tx), freshSeriesRequire(ctx, tx, spiceDB), seriesID)
}

func requireSeriesEdit(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, seriesID string) error {
	return series.RequireEditAndLock(ctx, seriespostgres.NewTranslationStore(tx), freshSeriesRequire(ctx, tx, spiceDB), seriesID)
}

// freshSeriesRequire checks an action with the principal's identity fence in
// the translation application's transaction.
func freshSeriesRequire(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient) mutation.Require {
	return func(can policyv1.Can) error {
		return identitystate.RequireFreshCan(ctx, tx, identitystate.PostgresFence{}, spiceDB, can)
	}
}
