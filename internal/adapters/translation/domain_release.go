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
		buildExtractionPlan: func(job core.PlanTarget, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return release.BuildTranslationExtractionPlan(releaseTranslationJob(job), source)
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
		requireTranslationRead:  requireTranslationEdit(policyv1.Release.Edit),
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

func releaseTranslationJob(job core.PlanTarget) release.TranslationJob {
	return release.TranslationJob{
		EntityType: job.EntityType, EntityID: job.EntityID, SourceLocale: job.SourceLocale,
		TargetLocale: job.TargetLocale,
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
