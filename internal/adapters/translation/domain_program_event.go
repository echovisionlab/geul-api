package translationadapter

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/programevent"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func programEventDomainRegistration(auditWriter domainaudit.Appender) domainRegistration {
	return newConfiguredDomainPort(core.KindProgramEvent, domainPortFunctions{
		loadSourceDocument: programevent.LoadTranslationSourceDocument,
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return core.BuildRichTextExtractionPlan(job, source, core.RichTextDocumentFields{Summary: true})
		},
		buildCandidate: core.BuildRichTextCandidate,
		applyCandidate: func(ctx context.Context, tx *gorm.DB, store *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, input core.EntryWrite) error {
			return programevent.ApplyTypedTranslationCandidateWithDB(ctx, tx, store, job, candidate, input, auditWriter)
		},
		appendSourceLocaleAudit: sourceLocaleAudit(auditWriter, sharedtelemetry.AuditProgramEventUpdated, sharedtelemetry.NewProgramEventSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, NULL::text AS title, summary, NULL::text AS content_html, NULL::text AS content_text, NULL::jsonb AS content_json, updated_at, NULL::uuid AS og_asset_id FROM %s`, table)
		},
		requireEditable: programevent.RequireExists,
		requireInterchangeView: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
			return programevent.RequireLockedView(ctx, tx, spiceDB, entityID)
		},
		requireInterchangeEdit: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
			return programevent.RequireLockedSourceLocaleEdit(ctx, tx, spiceDB, entityID)
		},
		requireJobRead: requireJobEdit(policyv1.ProgramEvent.Edit),
		requireSourceLocaleEdit: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
			return programevent.RequireLockedSourceLocaleEdit(ctx, tx, spiceDB, entityID)
		},
	})
}
