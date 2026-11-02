package translationadapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/release"
	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func releaseDomainRegistration(audit domainaudit.Appender) domainRegistration {
	return newConfiguredDomainPort(core.KindRelease, domainPortFunctions{
		loadSourceDocument: func(ctx context.Context, db *gorm.DB, store *contentblock.Store, releaseID string) (*core.SourceDocument, error) {
			if store == nil {
				return nil, errors.New("release translation content Block store is not configured")
			}
			source, err := release.LoadTypedTranslationSourceDocument(ctx, releaseStore(db, store), releaseID)
			return source, releaseConnectError(err)
		},
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			if job == nil {
				return nil, errors.New("typed Release translation source is required")
			}
			return release.BuildTranslationExtractionPlan(releaseTranslationJob(job), source)
		},
		buildCandidate: release.BuildTranslationCandidate,
		applyCandidate: func(ctx context.Context, tx *gorm.DB, store *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, input core.EntryWrite) error {
			if store == nil || job == nil {
				return errors.New("typed Release translation candidate and content Block store are required")
			}
			joined := releasepostgres.Bind(tx, releasepostgres.Ports{ContentBlocks: store}, audit)
			return releaseConnectError(release.ApplyTypedTranslationCandidate(ctx, joined, releaseTranslationJob(job), candidate, input))
		},
		sourceLocaleAudit: sourceLocaleAudit(sharedtelemetry.AuditReleaseUpdated, sharedtelemetry.NewReleaseSourceLocaleAuditRecord),
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
	return releaseConnectError(release.RequireLockedSourceLocaleEdit(ctx, releaseStore(tx, nil), func(principal *auth.UserInfo) (bool, error) {
		return identitystate.LockActivePrincipal(ctx, tx, principal)
	}, spiceDB, releaseID))
}

// releaseStore binds the Release Store to the translation domain's database
// handle, with the content store it hands the delivery.
func releaseStore(db *gorm.DB, contents *contentblock.Store) release.Store {
	return releasepostgres.NewStore(db, releasepostgres.Ports{ContentBlocks: contents})
}

func releaseTranslationJob(job *model.TranslationJob) release.TranslationJob {
	return release.TranslationJob{
		EntityType: job.EntityType, EntityID: job.EntityID, SourceLocale: job.SourceLocale,
		TargetLocale: job.TargetLocale, RequestedByMemberID: job.RequestedByMemberID,
	}
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
