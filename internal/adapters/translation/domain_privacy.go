package translationadapter

import (
	"context"
	"fmt"

	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	legalpostgres "github.com/echovisionlab/geul-api/internal/legal/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func privacyDomainRegistration(audit domainaudit.Appender) domainRegistration {
	return legalDomainRegistration(
		core.KindPrivacy,
		audit,
		policyv1.PrivacyHistory.Edit,
		legalSourceLocaleAudit(
			"privacy_history",
			"privacy",
			sharedtelemetry.NewPrivacySourceLocaleAuditRecord,
		),
	)
}

// legalDomainRegistration contains the shared translation behavior of the two
// policy-history aggregates. The aggregate kind remains explicit at every
// owning-domain boundary so a Privacy operation cannot accidentally target a
// Terms history (or vice versa).
func legalDomainRegistration(
	kind core.Kind,
	audit domainaudit.Appender,
	jobEdit func(string) (policyv1.Can, error),
	sourceLocaleAudit func(context.Context, *gorm.DB, string, string, string) (application.SourceLocaleAudit, error),
) domainRegistration {
	entityType := string(kind)
	return newConfiguredDomainPort(kind, domainPortFunctions{
		loadSourceDocument: func(ctx context.Context, db *gorm.DB, store *contentblock.Store, entityID string) (*core.SourceDocument, error) {
			return legaldomain.LoadTypedTranslationSourceDocument(ctx, legalJoined(db, store, nil).Store, entityType, entityID)
		},
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			if job == nil || job.EntityType != entityType {
				return nil, fmt.Errorf("%s translation requires %s", entityType, entityType)
			}
			return core.BuildRichTextExtractionPlan(jobIdentity(job), source, core.RichTextDocumentFields{Title: true})
		},
		buildCandidate: func(plan *core.ExtractionPlan, source *core.SourceDocument, results map[string]core.UnitResult) (*core.Candidate, error) {
			if plan == nil || plan.EntityType != entityType {
				return nil, fmt.Errorf("%s translation requires %s", entityType, entityType)
			}
			return core.BuildRichTextCandidate(plan, source, results)
		},
		applyCandidate: func(ctx context.Context, tx *gorm.DB, store *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, _ core.EntryWrite) error {
			if job == nil {
				// A missing job fails the candidate check, as a missing
				// candidate does.
				job, candidate = &model.TranslationJob{}, nil
			}
			return legaldomain.ApplyTypedTranslationCandidate(ctx, legalJoined(tx, store, audit), legalTranslationJob(job), candidate)
		},
		requestLocaleOG: func(ctx context.Context, tx *gorm.DB, planner *ogpostgres.Planner, _ *ogpostgres.Refresher, entityID string, locale string, reason string) (bool, error) {
			_, err := legaladapter.RequestSaved(ctx, tx, planner, entityType, entityID, locale, false, reason)
			return true, err
		},
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, title, NULL::text AS summary, content_html, content_text, NULL::jsonb AS content_json, updated_at, NULL::uuid AS og_asset_id FROM %s`, table)
		},
		sourceLocaleAudit: sourceLocaleAudit,
		requireInterchangeView: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
			return legaldomain.RequireTranslationInterchangeView(ctx, legalJoined(tx, nil, nil).Store, spiceDB, legalLockActive(ctx, tx), entityType, entityID)
		},
		requireInterchangeEdit: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
			return legaldomain.RequireEditableTranslationMutation(ctx, legalJoined(tx, nil, nil).Store, spiceDB, legalLockActive(ctx, tx), entityType, entityID)
		},
		requireJobRead: requireJobEdit(jobEdit),
		requireSourceLocaleEdit: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
			return legaldomain.RequireLockedSourceLocaleEdit(ctx, legalJoined(tx, nil, nil).Store, spiceDB, legalLockActive(ctx, tx), entityType, entityID)
		},
		requireRegeneration: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
			return legaldomain.RequireEditableTranslationMutation(ctx, legalJoined(tx, nil, nil).Store, spiceDB, legalLockActive(ctx, tx), entityType, entityID)
		},
	})
}

// legalJoined binds the Legal Store and audit to the translation domain's
// database handle, with the content store it hands the delivery.
func legalJoined(db *gorm.DB, contents *contentblock.Store, audit domainaudit.Appender) legaldomain.Joined {
	return legalpostgres.Bind(db, legalpostgres.Ports{ContentBlocks: contents}, audit)
}

// legalLockActive is the identity fence of the translation domain's
// transaction.
func legalLockActive(ctx context.Context, tx *gorm.DB) legaldomain.LockActive {
	return func(principal *auth.UserInfo) (bool, error) {
		return identitystate.LockActivePrincipal(ctx, tx, principal)
	}
}

func legalTranslationJob(job *model.TranslationJob) legaldomain.TranslationJob {
	return legaldomain.TranslationJob{
		EntityType: job.EntityType, EntityID: job.EntityID, SourceLocale: job.SourceLocale,
		TargetLocale: job.TargetLocale, RequestedByMemberID: job.RequestedByMemberID,
	}
}
