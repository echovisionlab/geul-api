package translationadapter

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/release"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func releaseDomainRegistration(auditWriter domainaudit.Appender) domainRegistration {
	return newConfiguredDomainPort(core.KindRelease, domainPortFunctions{
		loadSourceDocument: func(ctx context.Context, db *gorm.DB, store *contentblock.Store, releaseID string) (*core.SourceDocument, error) {
			source, err := release.LoadTypedTranslationSourceDocument(ctx, db, store, releaseID)
			return source, releaseConnectError(err)
		},
		buildExtractionPlan: release.BuildTranslationExtractionPlan,
		buildCandidate:      release.BuildTranslationCandidate,
		applyCandidate: func(ctx context.Context, tx *gorm.DB, store *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, input core.EntryWrite) error {
			return releaseConnectError(release.ApplyTypedTranslationCandidateWithDB(ctx, tx, store, job, candidate, input, auditWriter))
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
		requireInterchangeEdit:  requireLockedReleaseSourceLocaleEdit,
		requireJobRead:          requireJobEdit(policyv1.Release.Edit),
		requireSourceLocaleEdit: requireLockedReleaseSourceLocaleEdit,
	})
}

func requireLockedReleaseSourceLocaleEdit(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, releaseID string) error {
	return releaseConnectError(release.RequireLockedSourceLocaleEdit(ctx, tx, spiceDB, releaseID))
}

// releaseConnectError gives a Release use-case answer the Connect form the
// translation application branches on; an unclassified error passes
// through.
func releaseConnectError(err error) error {
	if classified, ok := errs.Classified(err); ok {
		return classified
	}
	return err
}
