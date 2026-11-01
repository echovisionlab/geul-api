package translationadapter

import (
	"context"
	"errors"
	"fmt"
	"time"

	menuadapter "github.com/echovisionlab/geul-api/internal/adapters/menu"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/menu"
	menupostgres "github.com/echovisionlab/geul-api/internal/menu/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func menuDomainRegistration(audit domainaudit.Appender) domainRegistration {
	return newConfiguredDomainPort(core.KindMenu, domainPortFunctions{
		loadSourceDocument: func(ctx context.Context, db *gorm.DB, _ *contentblock.Store, entityID string) (*core.SourceDocument, error) {
			return menu.LoadTranslationSourceDocument(ctx, menupostgres.NewStore(db, menupostgres.Ports{}), entityID)
		},
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return menu.BuildTranslationExtractionPlan(job.EntityID, job.SourceLocale, job.TargetLocale, source)
		},
		buildCandidate: func(_ *core.ExtractionPlan, source *core.SourceDocument, results map[string]core.UnitResult) (*core.Candidate, error) {
			return menu.BuildTranslationCandidate(source, results)
		},
		applyCandidate: func(ctx context.Context, tx *gorm.DB, _ *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, input core.EntryWrite) error {
			if job == nil || job.EntityType != string(core.KindMenu) {
				return errors.New("menu provider translation candidate is required")
			}
			return menu.ApplyProviderTranslation(ctx, menupostgres.Bind(tx, menupostgres.Ports{}, audit), menu.ProviderTranslation{
				MenuID: job.EntityID, TargetLocale: job.TargetLocale, RequestedByMemberID: job.RequestedByMemberID,
				Candidate: candidate, Now: input.Now,
			})
		},
		sourceLocaleAudit: sourceLocaleAudit(sharedtelemetry.AuditMenuUpdated, sharedtelemetry.NewMenuSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, NULL::text AS title, NULL::text AS summary, NULL::text AS content_html, NULL::text AS content_text, items_json AS content_json, updated_at, NULL::uuid AS og_asset_id FROM %s`, table)
		},
		requireInterchangeView: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, menuID string) error {
			return menu.RequireViewAndLock(ctx, menupostgres.NewStore(tx, menupostgres.Ports{}), freshMenuCan(ctx, tx, spiceDB), menuID)
		},
		requireSourceLocaleEdit: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, menuID string) error {
			return menu.RequireEditAndLock(ctx, menupostgres.NewStore(tx, menupostgres.Ports{}), freshMenuCan(ctx, tx, spiceDB), menuID)
		},
		requireJobRead:      requireJobEdit(policyv1.Menu.Edit),
		prepareSourceLocale: prepareMenuSourceLocale,
	})
}

func prepareMenuSourceLocale(
	ctx context.Context,
	db *gorm.DB,
	entityID string,
	_ string,
	requestedLocale string,
	now time.Time,
) error {
	if err := menuadapter.EnsureTranslationLocale(ctx, db, entityID, requestedLocale, now); err != nil {
		return errs.Internal(err)
	}
	return nil
}

// freshMenuCan runs the Menu's fresh check in the translation pipeline's
// transaction, through the production identity fence.
func freshMenuCan(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient) mutation.Require {
	return func(can policyv1.Can) error {
		return identitystate.RequireFreshCan(ctx, tx, identitystate.PostgresFence{}, spiceDB, can)
	}
}
