package translationadapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func emailTemplateDomainRegistration(
	references emailauthoringpostgres.DeliveryReferences,
	audit domainaudit.Appender,
) domainRegistration {
	return newConfiguredDomainPort(core.KindEmailTemplate, domainPortFunctions{
		loadSourceDocument: func(ctx context.Context, db *gorm.DB, store *contentblock.Store, templateID string) (*core.SourceDocument, error) {
			return emailauthoring.LoadTemplateTranslationSourceDocument(ctx, emailAuthoringStore(db, store, references), templateID)
		},
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return core.BuildRichTextExtractionPlan(jobIdentity(job), source, core.RichTextDocumentFields{Title: true})
		},
		buildCandidate: core.BuildRichTextCandidate,
		applyCandidate: func(
			ctx context.Context,
			tx *gorm.DB,
			store *contentblock.Store,
			job *model.TranslationJob,
			candidate *core.Candidate,
			input core.EntryWrite,
		) error {
			if job == nil {
				return errors.New("typed Email Template translation candidate is required")
			}
			return emailauthoring.ApplyTemplateTranslationCandidate(
				ctx, emailauthoringpostgres.Bind(tx, emailAuthoringPorts(store, references), audit),
				emailContentTranslationJob(job), candidate, input,
			)
		},
		sourceLocaleAudit: sourceLocaleAudit(sharedtelemetry.AuditEmailTemplateUpdated, sharedtelemetry.NewEmailTemplateSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(
				`SELECT locale, subject AS title, NULL::text AS summary, content_html, content_text, NULL::jsonb AS content_json, updated_at, NULL::uuid AS og_asset_id FROM %s`,
				table,
			)
		},
		requireInterchangeView: genericInterchangeView(
			core.KindEmailTemplate,
			translationCanSet{view: policyv1.EmailTemplate.View, edit: policyv1.EmailTemplate.Edit},
			"",
		),
		requireSourceLocaleEdit: genericSourceLocaleEdit(
			core.KindEmailTemplate,
			translationCanSet{view: policyv1.EmailTemplate.View, edit: policyv1.EmailTemplate.Edit},
			"",
		),
		requireJobRead: requireJobEdit(policyv1.EmailTemplate.Edit),
		requireSourceMutable: func(ctx context.Context, db *gorm.DB, entityID string) error {
			return emailauthoring.RequireTranslationSourceMutable(
				ctx, emailAuthoringStore(db, nil, references), string(core.KindEmailTemplate), entityID,
			)
		},
	})
}
