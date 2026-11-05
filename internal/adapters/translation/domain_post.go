package translationadapter

import (
	"fmt"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	postpostgres "github.com/echovisionlab/geul-api/internal/post/postgres"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func postDomainRegistration(audit domainaudit.Appender) domainRegistration {
	return newConfiguredDomainPort(core.KindPost, domainPortFunctions{
		loadSourceDocument: postpostgres.LoadTypedTranslationSourceDocument,
		buildExtractionPlan: func(job core.PlanTarget, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return core.BuildRichTextExtractionPlan(&job, source, core.RichTextDocumentFields{Title: true, Summary: true})
		},
		requestLocaleOG:   localeAwareOG(core.KindPost),
		sourceLocaleAudit: sourceLocaleAudit(sharedtelemetry.AuditPostUpdated, sharedtelemetry.NewPostSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, title, summary, NULL::text AS content_html, NULL::text AS content_text, NULL::jsonb AS content_json, updated_at, og_asset_id FROM %s`, table)
		},
		requireInterchangeView:  postpostgres.RequireLockedView,
		requireSourceLocaleEdit: postpostgres.RequireLockedSourceLocaleEdit,
		requireTranslationRead:  requireTranslationEdit(policyv1.Post.Edit),
	})
}
