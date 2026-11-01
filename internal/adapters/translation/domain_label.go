package translationadapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/label"
	labelpostgres "github.com/echovisionlab/geul-api/internal/label/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func labelDomainRegistration(audit domainaudit.Appender) domainRegistration {
	return newConfiguredDomainPort(core.KindLabel, domainPortFunctions{
		loadSourceDocument: func(ctx context.Context, db *gorm.DB, store *contentblock.Store, labelID string) (*core.SourceDocument, error) {
			if store == nil {
				return nil, errors.New("label translation content Block store is not configured")
			}
			return label.LoadTypedTranslationSourceDocument(ctx, labelJoined(db, store, nil), labelID)
		},
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			if job == nil || job.EntityType != string(core.KindLabel) {
				return nil, errors.New("typed Label translation source is required")
			}
			return core.BuildRichTextExtractionPlan(job, source, core.RichTextDocumentFields{})
		},
		buildCandidate: func(plan *core.ExtractionPlan, source *core.SourceDocument, results map[string]core.UnitResult) (*core.Candidate, error) {
			if plan == nil || plan.EntityType != string(core.KindLabel) {
				return nil, errors.New("typed Label translation source is required")
			}
			return core.BuildRichTextCandidate(plan, source, results)
		},
		applyCandidate: func(ctx context.Context, tx *gorm.DB, store *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, input core.EntryWrite) error {
			if store == nil || job == nil {
				return errors.New("typed Label translation candidate and content Block store are required")
			}
			return label.ApplyTypedTranslationCandidate(ctx, labelJoined(tx, store, audit), labelTranslationJob(job), candidate, input)
		},
		sourceLocaleAudit: sourceLocaleAudit(sharedtelemetry.AuditLabelUpdated, sharedtelemetry.NewLabelSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, NULL::text AS title, NULL::text AS summary, content_html, content_text, NULL::jsonb AS content_json, updated_at, NULL::uuid AS og_asset_id FROM %s`, table)
		},
		requireInterchangeView: genericInterchangeView(
			core.KindLabel,
			translationCanSet{view: policyv1.Label.View, edit: policyv1.Label.Edit},
			"",
		),
		requireSourceLocaleEdit: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, labelID string) error {
			return label.RequireLockedSourceLocaleEdit(ctx, labelJoined(tx, nil, nil), spiceDB, func(principal *auth.UserInfo) (bool, error) {
				return identitystate.LockActivePrincipal(ctx, tx, principal)
			}, labelID)
		},
		requireJobRead: requireJobEdit(policyv1.Label.Edit),
	})
}

// labelJoined binds the Label Store to the translation domain's database
// handle, with the content store it hands the delivery.
func labelJoined(db *gorm.DB, contents *contentblock.Store, audit domainaudit.Appender) label.Joined {
	return labelpostgres.Bind(db, labelpostgres.Ports{ContentBlocks: contents}, audit)
}

func labelTranslationJob(job *model.TranslationJob) label.TranslationJob {
	return label.TranslationJob{
		EntityType: job.EntityType, EntityID: job.EntityID, TargetLocale: job.TargetLocale,
		RequestedByMemberID: job.RequestedByMemberID,
	}
}
