package translationadapter

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func labelDomainRegistration(auditWriter domainaudit.Appender) domainRegistration {
	return newConfiguredDomainPort(core.KindLabel, domainPortFunctions{
		loadSourceDocument: label.LoadTypedTranslationSourceDocument,
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return label.BuildTranslationExtractionPlan(job, source)
		},
		buildCandidate: label.BuildTranslationCandidate,
		applyCandidate: func(ctx context.Context, tx *gorm.DB, store *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, input core.EntryWrite) error {
			return label.ApplyTypedTranslationCandidateWithDB(ctx, tx, store, job, candidate, input, auditWriter)
		},
		appendSourceLocaleAudit: sourceLocaleAudit(auditWriter, sharedtelemetry.AuditLabelUpdated, sharedtelemetry.NewLabelSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, NULL::text AS title, NULL::text AS summary, content_html, content_text, NULL::jsonb AS content_json, updated_at, NULL::uuid AS og_asset_id FROM %s`, table)
		},
		requireInterchangeView: genericInterchangeView(
			core.KindLabel,
			translationCanSet{view: policyv1.Label.View, edit: policyv1.Label.Edit},
			"",
		),
		requireSourceLocaleEdit: label.RequireLockedSourceLocaleEdit,
		requireJobRead:          requireJobEdit(policyv1.Label.Edit),
	})
}
