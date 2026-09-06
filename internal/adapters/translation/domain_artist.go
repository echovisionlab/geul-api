package translationadapter

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func artistDomainRegistration(auditWriter domainaudit.Appender) domainRegistration {
	return newConfiguredDomainPort(core.KindArtist, domainPortFunctions{
		loadSourceDocument: artist.LoadTypedTranslationSourceDocument,
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return artist.BuildTranslationExtractionPlan(job, source)
		},
		buildCandidate: artist.BuildTranslationCandidate,
		applyCandidate: func(ctx context.Context, tx *gorm.DB, store *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, input core.EntryWrite) error {
			return artist.ApplyTypedTranslationCandidateWithDB(ctx, tx, store, job, candidate, input, auditWriter)
		},
		requestLocaleOG:         localeAwareOG(core.KindArtist),
		appendSourceLocaleAudit: sourceLocaleAudit(auditWriter, sharedtelemetry.AuditArtistUpdated, sharedtelemetry.NewArtistSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, NULL::text AS title, NULL::text AS summary, content_html, content_text, NULL::jsonb AS content_json, updated_at, NULL::uuid AS og_asset_id FROM %s`, table)
		},
		requireInterchangeView: genericInterchangeView(
			core.KindArtist,
			translationCanSet{view: policyv1.Artist.View, edit: policyv1.Artist.Edit},
			"",
		),
		requireSourceLocaleEdit: artist.RequireLockedSourceLocaleEdit,
		requireJobRead:          requireJobEdit(policyv1.Artist.Edit),
	})
}
