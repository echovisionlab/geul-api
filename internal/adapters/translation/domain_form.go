package translationadapter

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/form"
	formpostgres "github.com/echovisionlab/geul-api/internal/form/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func formDomainRegistration(audit domainaudit.Appender) domainRegistration {
	permissions := translationCanSet{
		view: policyv1.Form.View,
		edit: policyv1.Form.Edit,
	}
	return newConfiguredDomainPort(core.KindForm, domainPortFunctions{
		loadSourceDocument: func(ctx context.Context, db *gorm.DB, _ *contentblock.Store, entityID string) (*core.SourceDocument, error) {
			return form.LoadTranslationSourceDocument(ctx, formStore(db, nil), entityID)
		},
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return form.BuildTranslationExtractionPlan(job.EntityID, job.SourceLocale, job.TargetLocale, source)
		},
		buildCandidate: func(plan *core.ExtractionPlan, source *core.SourceDocument, results map[string]core.UnitResult) (*core.Candidate, error) {
			return form.ApplyTranslationCandidate(source, results)
		},
		applyCandidate: func(ctx context.Context, tx *gorm.DB, store *contentblock.Store, job *model.TranslationJob, candidate *core.Candidate, input core.EntryWrite) error {
			if tx == nil || store == nil || job == nil || candidate == nil {
				return errors.New("form provider translation dependencies are required")
			}
			input.Title = candidate.Title
			input.Summary = candidate.Summary
			input.ContentJSON = candidate.ContentJSON
			input.ContentHTML = candidate.ContentHTML
			input.ContentText = candidate.ContentText
			return form.ApplyProviderTranslationCandidate(ctx, formpostgres.Bind(tx, formpostgres.Ports{ContentBlocks: store}, audit), form.TranslationJob{
				EntityType: job.EntityType, EntityID: job.EntityID, TargetLocale: job.TargetLocale,
				RequestedByMemberID: job.RequestedByMemberID,
			}, candidate, input)
		},
		sourceLocaleAudit: sourceLocaleAudit(sharedtelemetry.AuditFormUpdated, sharedtelemetry.NewFormSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(string) string {
			return formpostgres.TranslationEntrySelectSQL()
		},
		requestLocaleOG: localeAwareOG(core.KindForm),
		requireInterchangeView: genericInterchangeView(
			core.KindForm,
			permissions,
			"",
		),
		requireInterchangeEdit: genericSourceLocaleEdit(
			core.KindForm,
			permissions,
			"",
		),
		requireJobRead: requireJobEdit(policyv1.Form.Edit),
		requireSourceLocaleEdit: genericSourceLocaleEdit(
			core.KindForm,
			permissions,
			"",
		),
		requireRegeneration: genericSourceLocaleEdit(
			core.KindForm,
			permissions,
			"",
		),
		prepareSourceLocale: func(ctx context.Context, db *gorm.DB, entityID string, currentSourceLocale string, requestedLocale string, now time.Time) error {
			if db == nil {
				return form.PrepareSourceLocaleSwitch(ctx, nil, entityID, currentSourceLocale, requestedLocale, now)
			}
			return form.PrepareSourceLocaleSwitch(ctx, formStore(db, nil), entityID, currentSourceLocale, requestedLocale, now)
		},
	})
}

// formStore binds the Form Store to the translation domain's database
// handle, with the content store it hands the delivery.
func formStore(db *gorm.DB, contents *contentblock.Store) form.Store {
	return formpostgres.NewStore(db, formpostgres.Ports{ContentBlocks: contents})
}
