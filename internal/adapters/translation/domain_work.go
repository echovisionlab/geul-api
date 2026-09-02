package translationadapter

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/work"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func workDomainRegistration(auditWriter domainaudit.Appender) domainRegistration {
	permissions := translationCanSet{
		view:         policyv1.Work.View,
		edit:         policyv1.Work.Edit,
		viewArchived: policyv1.Work.ViewArchived,
		editArchived: policyv1.Work.EditArchived,
	}
	archivedStatus := managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()
	return newConfiguredDomainPort(core.KindWork, domainPortFunctions{
		loadSourceDocument: work.LoadTypedTranslationSourceDocument,
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return work.BuildTranslationExtractionPlan(job, source)
		},
		buildCandidate: work.BuildTranslationCandidate,
		applyCandidate: func(ctx context.Context, tx *gorm.DB, store *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, input core.EntryWrite) error {
			return work.ApplyTypedTranslationCandidateWithDB(ctx, tx, store, job, candidate, input, auditWriter)
		},
		requestLocaleOG:         localeAwareOG(core.KindWork),
		appendSourceLocaleAudit: sourceLocaleAudit(auditWriter, sharedtelemetry.AuditWorkUpdated, sharedtelemetry.NewWorkSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, title, summary, NULL::text AS content_html, NULL::text AS content_text, NULL::jsonb AS content_json, updated_at, NULL::uuid AS og_asset_id FROM %s`, table)
		},
		requireEditable: func(ctx context.Context, db *gorm.DB, entityID string) error {
			return work.RequireExists(ctx, db, entityID)
		},
		requireInterchangeView:  genericInterchangeView(core.KindWork, permissions, archivedStatus),
		requireSourceLocaleEdit: genericSourceLocaleEdit(core.KindWork, permissions, archivedStatus),
		requireJobRead:          requireJobEdit(policyv1.Work.Edit),
	})
}
