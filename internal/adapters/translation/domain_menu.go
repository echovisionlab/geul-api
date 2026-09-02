package translationadapter

import (
	"context"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func menuDomainRegistration(auditWriter domainaudit.Appender) domainRegistration {
	return newConfiguredDomainPort(core.KindMenu, domainPortFunctions{
		loadSourceDocument: func(ctx context.Context, db *gorm.DB, _ *contentblock.Store, entityID string) (*core.SourceDocument, error) {
			return menu.LoadTranslationSourceDocument(ctx, db, entityID)
		},
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return menu.BuildTranslationExtractionPlan(job.EntityID, job.SourceLocale, job.TargetLocale, source)
		},
		buildCandidate: func(_ *core.ExtractionPlan, source *core.SourceDocument, results map[string]core.UnitResult) (*core.Candidate, error) {
			return menu.BuildTranslationCandidate(source, results)
		},
		applyCandidate: func(ctx context.Context, tx *gorm.DB, _ *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, input core.EntryWrite) error {
			input.Title = candidate.Title
			input.Summary = candidate.Summary
			input.ContentJSON = candidate.ContentJSON
			input.ContentHTML = candidate.ContentHTML
			input.ContentText = candidate.ContentText
			return menu.ApplyProviderTranslationCandidateWithDB(ctx, tx, job, candidate, input, auditWriter)
		},
		appendSourceLocaleAudit: sourceLocaleAudit(auditWriter, sharedtelemetry.AuditMenuUpdated, sharedtelemetry.NewMenuSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, NULL::text AS title, NULL::text AS summary, NULL::text AS content_html, NULL::text AS content_text, items_json AS content_json, updated_at, NULL::uuid AS og_asset_id FROM %s`, table)
		},
		requireInterchangeView:  menu.RequireViewAndLockWithDB,
		requireSourceLocaleEdit: menu.RequireEditAndLockWithDB,
		requireJobRead:          requireJobEdit(policyv1.Menu.Edit),
		prepareSourceLocale:     prepareMenuSourceLocale,
	})
}

func prepareMenuSourceLocale(
	ctx context.Context,
	db *gorm.DB,
	entityID string,
	_ string,
	requestedLocale string,
	now time.Time,
) error {
	empty, err := menu.EncodeTranslationLabelValues(nil)
	if err != nil {
		return errs.Internal(err)
	}
	result := db.WithContext(ctx).Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (entity_id, locale) DO NOTHING`,
		entityID,
		requestedLocale,
		string(empty),
		now.UTC(),
		now.UTC(),
	)
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	return nil
}
