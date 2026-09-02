package translationadapter

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/series"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func seriesDomainRegistration(auditWriter domainaudit.Appender) domainRegistration {
	return newConfiguredDomainPort(core.KindPostSeries, domainPortFunctions{
		loadSourceDocument: func(ctx context.Context, db *gorm.DB, _ *contentblock.Store, entityID string) (*core.SourceDocument, error) {
			return series.LoadTranslationSourceDocument(ctx, db, entityID)
		},
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return series.BuildTranslationExtractionPlan(job.EntityID, job.SourceLocale, job.TargetLocale, source)
		},
		buildCandidate: application.BuildGenericCandidate,
		applyCandidate: func(ctx context.Context, tx *gorm.DB, _ *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, input core.EntryWrite) error {
			input.Title = candidate.Title
			input.Summary = candidate.Summary
			input.ContentJSON = candidate.ContentJSON
			input.ContentHTML = candidate.ContentHTML
			input.ContentText = candidate.ContentText
			return series.ApplyProviderTranslationCandidateWithDB(ctx, tx, job, candidate, input, auditWriter)
		},
		requestLocaleOG: localeAwareOG(core.KindPostSeries),
		appendSourceLocaleAudit: sourceLocaleAudit(
			auditWriter, sharedtelemetry.AuditPostSeriesUpdated, sharedtelemetry.NewPostSeriesSourceLocaleAuditRecord,
		),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, title, summary, content_html, content_text, content_json, updated_at, og_asset_id FROM %s`, table)
		},
		requireInterchangeView:  series.RequireViewAndLockWithDB,
		requireSourceLocaleEdit: series.RequireEditAndLockWithDB,
		requireJobRead: func(ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
			err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				return series.RequireViewAndLockWithDB(ctx, tx, spiceDB, entityID)
			})
			return maskTranslationAuthorizationDenial(err, string(core.KindPostSeries), entityID)
		},
	})
}
