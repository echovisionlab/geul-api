package translationadapter

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func campaignDomainRegistration(auditWriter domainaudit.Appender) domainRegistration {
	permissions := translationCanSet{
		view: policyv1.Campaign.View,
		edit: policyv1.Campaign.Edit,
	}
	return newConfiguredDomainPort(core.KindCampaign, domainPortFunctions{
		loadSourceDocument: campaign.LoadTranslationSourceDocument,
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return core.BuildRichTextExtractionPlan(job, source, core.RichTextDocumentFields{Title: true})
		},
		buildCandidate: core.BuildRichTextCandidate,
		applyCandidate: func(ctx context.Context, tx *gorm.DB, store *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, input core.EntryWrite) error {
			return campaign.ApplyTranslationCandidate(ctx, tx, store, job, candidate, input, auditWriter)
		},
		appendSourceLocaleAudit: sourceLocaleAudit(auditWriter, sharedtelemetry.AuditCampaignUpdated, sharedtelemetry.NewCampaignSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, subject AS title, NULL::text AS summary, content_html, content_text, NULL::jsonb AS content_json, updated_at, NULL::uuid AS og_asset_id FROM %s`, table)
		},
		requireInterchangeView: genericInterchangeView(
			core.KindCampaign,
			permissions,
			"",
		),
		requireInterchangeEdit: genericSourceLocaleEdit(
			core.KindCampaign,
			permissions,
			"",
		),
		requireJobRead: requireJobEdit(policyv1.Campaign.Edit),
		requireSourceLocaleEdit: genericSourceLocaleEdit(
			core.KindCampaign,
			permissions,
			"",
		),
		requireRegeneration: genericSourceLocaleEdit(
			core.KindCampaign,
			permissions,
			"",
		),
		requireSourceMutable: campaign.RequireTranslationSourceMutable,
		requestLocaleOG:      noLocaleOG,
	})
}
