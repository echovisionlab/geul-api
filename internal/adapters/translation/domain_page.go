package translationadapter

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/page"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func pageDomainRegistration(auditWriter domainaudit.Appender) domainRegistration {
	return newConfiguredDomainPort(core.KindPage, domainPortFunctions{
		loadSourceDocument: page.LoadTranslationSourceDocument,
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return page.BuildTranslationExtractionPlan(job, source)
		},
		buildCandidate: page.BuildTranslationCandidate,
		applyCandidate: func(ctx context.Context, tx *gorm.DB, store *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, input core.EntryWrite) error {
			return page.ApplyTranslationCandidateWithDB(ctx, tx, store, job, candidate, input, auditWriter)
		},
		requestLocaleOG:         localeAwareOG(core.KindPage),
		appendSourceLocaleAudit: sourceLocaleAudit(auditWriter, sharedtelemetry.AuditPageUpdated, sharedtelemetry.NewPageSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, title, summary, NULL::text AS content_html, NULL::text AS content_text, NULL::jsonb AS content_json, updated_at, og_asset_id FROM %s`, table)
		},
		requireInterchangeView: genericInterchangeView(
			core.KindPage,
			translationCanSet{view: policyv1.Page.View, edit: policyv1.Page.Edit},
			"",
		),
		requireSourceLocaleEdit: genericSourceLocaleEdit(
			core.KindPage,
			translationCanSet{view: policyv1.Page.View, edit: policyv1.Page.Edit},
			"",
		),
		requireJobRead: requireJobEdit(policyv1.Page.Edit),
	})
}
