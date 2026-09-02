package translationadapter

import (
	"context"
	"fmt"

	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func privacyDomainRegistration(auditWriter domainaudit.Appender) domainRegistration {
	return legalDomainRegistration(
		core.KindPrivacy,
		auditWriter,
		policyv1.PrivacyHistory.Edit,
		legalSourceLocaleAudit(
			auditWriter,
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
	auditWriter domainaudit.Appender,
	jobEdit func(string) (policyv1.Can, error),
	appendSourceLocaleAudit func(context.Context, *gorm.DB, string, string, string) error,
) domainRegistration {
	entityType := string(kind)
	return newConfiguredDomainPort(kind, domainPortFunctions{
		loadSourceDocument: func(ctx context.Context, db *gorm.DB, store *contentblock.Store, entityID string) (*core.SourceDocument, error) {
			return legaldomain.LoadTypedTranslationSourceDocument(ctx, db, store, entityType, entityID)
		},
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			if job == nil || job.EntityType != entityType {
				return nil, fmt.Errorf("%s translation requires %s", entityType, entityType)
			}
			return legaldomain.BuildTranslationExtractionPlan(job, source)
		},
		buildCandidate: func(plan *core.ExtractionPlan, source *core.SourceDocument, results map[string]core.UnitResult) (*core.Candidate, error) {
			if plan == nil || plan.EntityType != entityType {
				return nil, fmt.Errorf("%s translation requires %s", entityType, entityType)
			}
			return legaldomain.BuildTranslationCandidate(plan, source, results)
		},
		applyCandidate: func(ctx context.Context, tx *gorm.DB, store *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, _ core.EntryWrite) error {
			return legaldomain.ApplyTypedTranslationCandidateWithDB(ctx, tx, store, job, candidate, auditWriter)
		},
		requestLocaleOG: func(ctx context.Context, tx *gorm.DB, planner *og.Planner, _ *og.Refresher, entityID string, locale string, reason string) (bool, error) {
			_, err := legaladapter.RequestSaved(ctx, tx, planner, entityType, entityID, locale, false, reason)
			return true, err
		},
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, title, NULL::text AS summary, content_html, content_text, NULL::jsonb AS content_json, updated_at, NULL::uuid AS og_asset_id FROM %s`, table)
		},
		appendSourceLocaleAudit: appendSourceLocaleAudit,
		requireInterchangeView: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
			return legaldomain.RequireTranslationInterchangeViewWithDB(ctx, tx, spiceDB, entityType, entityID)
		},
		requireInterchangeEdit: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
			return legaldomain.RequireEditableTranslationMutationWithDB(ctx, tx, spiceDB, entityType, entityID)
		},
		requireJobRead: requireJobEdit(jobEdit),
		requireSourceLocaleEdit: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
			return legaldomain.RequireLockedSourceLocaleEdit(ctx, tx, spiceDB, entityType, entityID)
		},
		requireRegeneration: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
			return legaldomain.RequireEditableTranslationMutationWithDB(ctx, tx, spiceDB, entityType, entityID)
		},
	})
}
