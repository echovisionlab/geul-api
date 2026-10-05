package translationadapter

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/release"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func releaseDomainRegistration(auditWriter domainaudit.Appender) domainRegistration {
	return newConfiguredDomainPort(core.KindRelease, domainPortFunctions{
		loadSourceDocument:  release.LoadTypedTranslationSourceDocument,
		buildExtractionPlan: release.BuildTranslationExtractionPlan,
		buildCandidate:      release.BuildTranslationCandidate,
		applyCandidate: func(ctx context.Context, tx *gorm.DB, store *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, input core.EntryWrite) error {
			return release.ApplyTypedTranslationCandidateWithDB(ctx, tx, store, job, candidate, input, auditWriter)
		},
		appendSourceLocaleAudit: sourceLocaleAudit(auditWriter, sharedtelemetry.AuditReleaseUpdated, sharedtelemetry.NewReleaseSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, NULL::text AS title, NULL::text AS summary, NULL::text AS content_html, NULL::text AS content_text, NULL::jsonb AS content_json, updated_at, NULL::uuid AS og_asset_id FROM %s`, table)
		},
		requireInterchangeView: genericInterchangeView(
			core.KindRelease,
			translationCanSet{view: policyv1.Release.View, edit: policyv1.Release.Edit},
			"",
		),
		requireInterchangeEdit:  release.RequireLockedSourceLocaleEdit,
		requireJobRead:          requireJobEdit(policyv1.Release.Edit),
		requireSourceLocaleEdit: release.RequireLockedSourceLocaleEdit,
	})
}
