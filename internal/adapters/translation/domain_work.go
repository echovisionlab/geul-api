package translationadapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/work"
	workpostgres "github.com/echovisionlab/geul-api/internal/work/postgres"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func workDomainRegistration(audit domainaudit.Appender) domainRegistration {
	permissions := translationCanSet{
		view:         policyv1.Work.View,
		edit:         policyv1.Work.Edit,
		viewArchived: policyv1.Work.ViewArchived,
		editArchived: policyv1.Work.EditArchived,
	}
	archivedStatus := string(model.WorkStatusArchived)
	return newConfiguredDomainPort(core.KindWork, domainPortFunctions{
		loadSourceDocument: func(ctx context.Context, db *gorm.DB, store *contentblock.Store, workID string) (*core.SourceDocument, error) {
			if store == nil {
				return nil, errors.New("work translation content Block store is not configured")
			}
			return work.LoadTypedTranslationSourceDocument(ctx, workStore(db, store), workID)
		},
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return work.BuildTranslationExtractionPlan(workTranslationJob(job), source)
		},
		buildCandidate: work.BuildTranslationCandidate,
		applyCandidate: func(ctx context.Context, tx *gorm.DB, store *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, input core.EntryWrite) error {
			if store == nil || job == nil {
				return errors.New("typed Work translation candidate and content Block store are required")
			}
			joined := workpostgres.Bind(tx, workpostgres.Ports{ContentBlocks: store}, audit)
			return work.ApplyTypedTranslationCandidate(ctx, joined, *workTranslationJob(job), candidate, input)
		},
		requestLocaleOG:   localeAwareOG(core.KindWork),
		sourceLocaleAudit: sourceLocaleAudit(sharedtelemetry.AuditWorkUpdated, sharedtelemetry.NewWorkSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, title, summary, NULL::text AS content_html, NULL::text AS content_text, NULL::jsonb AS content_json, updated_at, NULL::uuid AS og_asset_id FROM %s`, table)
		},
		requireEditable: func(ctx context.Context, db *gorm.DB, entityID string) error {
			return work.RequireExists(ctx, workStore(db, nil), entityID)
		},
		requireInterchangeView:  genericInterchangeView(core.KindWork, permissions, archivedStatus),
		requireSourceLocaleEdit: genericSourceLocaleEdit(core.KindWork, permissions, archivedStatus),
		requireJobRead:          requireJobEdit(policyv1.Work.Edit),
	})
}

// workStore binds the Work Store to the translation domain's database
// handle, with the content store it hands the delivery.
func workStore(db *gorm.DB, contents *contentblock.Store) work.Store {
	return workpostgres.Bind(db, workpostgres.Ports{ContentBlocks: contents}, nil).Store
}

func workTranslationJob(job *model.TranslationJob) *work.TranslationJob {
	if job == nil {
		return nil
	}
	return &work.TranslationJob{
		EntityType: job.EntityType, EntityID: job.EntityID, SourceLocale: job.SourceLocale,
		TargetLocale: job.TargetLocale, RequestedByMemberID: job.RequestedByMemberID,
	}
}
