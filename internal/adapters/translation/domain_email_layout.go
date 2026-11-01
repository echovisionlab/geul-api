package translationadapter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/email"
	emailpostgres "github.com/echovisionlab/geul-api/internal/email/postgres"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func emailLayoutDomainRegistration(
	references emailauthoringpostgres.DeliveryReferences,
	audit domainaudit.Appender,
) domainRegistration {
	return newConfiguredDomainPort(core.KindEmailLayout, domainPortFunctions{
		loadSourceDocument: func(
			ctx context.Context,
			db *gorm.DB,
			_ *contentblock.Store,
			entityID string,
		) (*core.SourceDocument, error) {
			locale, found, err := email.ResolveLayoutTranslationSourceLocale(ctx, emailpostgres.Bind(db), entityID)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, fmt.Errorf("email layout %q source locale is not initialized", entityID)
			}
			return email.LoadLayoutTranslationSourceDocument(ctx, emailpostgres.Bind(db), entityID, locale)
		},
		buildExtractionPlan: func(job *model.TranslationJob, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return email.BuildLayoutTranslationExtractionPlan(
				job.EntityID, job.SourceLocale, job.TargetLocale, source,
			)
		},
		buildCandidate: application.BuildGenericCandidate,
		applyCandidate: func(
			ctx context.Context,
			tx *gorm.DB,
			store *contentblock.Store,
			job *model.TranslationJob,
			candidate *core.Candidate,
			input core.EntryWrite,
		) error {
			input.Title = candidate.Title
			input.Summary = candidate.Summary
			input.ContentJSON = candidate.ContentJSON
			input.ContentHTML = candidate.ContentHTML
			input.ContentText = candidate.ContentText
			if job == nil {
				return errors.New("email layout provider translation dependencies are required")
			}
			return emailauthoring.ApplyEmailLayoutProviderTranslationCandidate(
				ctx, emailauthoringpostgres.Bind(tx, emailAuthoringPorts(store, references), audit),
				emailContentTranslationJob(job), candidate, input,
			)
		},
		sourceLocaleAudit: sourceLocaleAudit(sharedtelemetry.AuditEmailLayoutUpdated, sharedtelemetry.NewEmailLayoutSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return emailpostgres.LayoutTranslationEntrySelectSQL()
		},
		requireInterchangeView: genericInterchangeView(
			core.KindEmailLayout,
			translationCanSet{view: policyv1.EmailLayout.View, edit: policyv1.EmailLayout.Edit},
			"",
		),
		requireSourceLocaleEdit: genericSourceLocaleEdit(
			core.KindEmailLayout,
			translationCanSet{view: policyv1.EmailLayout.View, edit: policyv1.EmailLayout.Edit},
			"",
		),
		requireJobRead: requireJobEdit(policyv1.EmailLayout.Edit),
		prepareSourceLocale: func(
			ctx context.Context,
			db *gorm.DB,
			entityID string,
			currentSourceLocale string,
			requestedLocale string,
			now time.Time,
		) error {
			return email.PrepareLayoutSourceLocaleSwitch(
				ctx, emailpostgres.Bind(db), entityID, currentSourceLocale, requestedLocale, now,
			)
		},
		requireSourceMutable: func(ctx context.Context, db *gorm.DB, entityID string) error {
			return emailauthoring.RequireTranslationSourceMutable(
				ctx, emailAuthoringStore(db, nil, references), string(core.KindEmailLayout), entityID,
			)
		},
	})
}
