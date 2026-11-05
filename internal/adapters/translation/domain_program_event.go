package translationadapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/programevent"
	programeventpostgres "github.com/echovisionlab/geul-api/internal/programevent/postgres"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func programEventDomainRegistration(audit domainaudit.Appender) domainRegistration {
	return newConfiguredDomainPort(core.KindProgramEvent, domainPortFunctions{
		loadSourceDocument: func(ctx context.Context, db *gorm.DB, store *contentblock.Store, eventID string) (*core.SourceDocument, error) {
			if store == nil {
				return nil, errors.New("program Event translation content Block store is not configured")
			}
			var output *core.SourceDocument
			err := mutation.ReadCurrent(ctx, db, func(tx *gorm.DB) error {
				var err error
				output, err = programevent.LoadTranslationSourceDocument(ctx, programEventStore(tx, store), eventID)
				return err
			})
			return output, err
		},
		buildExtractionPlan: func(job core.PlanTarget, source *core.SourceDocument) (*core.ExtractionPlan, error) {
			return core.BuildRichTextExtractionPlan(&job, source, core.RichTextDocumentFields{Summary: true})
		},
		sourceLocaleAudit: sourceLocaleAudit(sharedtelemetry.AuditProgramEventUpdated, sharedtelemetry.NewProgramEventSourceLocaleAuditRecord),
		translationEntrySelectSQL: func(table string) string {
			return fmt.Sprintf(`SELECT locale, NULL::text AS title, summary, NULL::text AS content_html, NULL::text AS content_text, NULL::jsonb AS content_json, updated_at, NULL::uuid AS og_asset_id FROM %s`, table)
		},
		requireEditable: func(ctx context.Context, db *gorm.DB, entityID string) error {
			return programEventAnswer(programevent.RequireExists(ctx, programEventStore(db, nil), entityID))
		},
		requireInterchangeView: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
			return programEventAnswer(programevent.RequireLockedView(ctx, programEventStore(tx, nil), spiceDB, programEventLockActive(ctx, tx), entityID))
		},
		requireInterchangeEdit: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
			return programEventAnswer(programevent.RequireLockedSourceLocaleEdit(ctx, programEventStore(tx, nil), spiceDB, programEventLockActive(ctx, tx), entityID))
		},
		requireTranslationRead: requireTranslationEdit(policyv1.ProgramEvent.Edit),
		requireSourceLocaleEdit: func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
			return programEventAnswer(programevent.RequireLockedSourceLocaleEdit(ctx, programEventStore(tx, nil), spiceDB, programEventLockActive(ctx, tx), entityID))
		},
	})
}

// programEventJoined binds the Program Event Store and audit to the
// translation domain's database handle, with the content store it hands
// the delivery.
func programEventJoined(db *gorm.DB, contents *contentblock.Store, audit domainaudit.Appender) programevent.Joined {
	return programeventpostgres.Bind(db, programeventpostgres.Ports{ContentBlocks: contents}, audit)
}

func programEventStore(db *gorm.DB, contents *contentblock.Store) programevent.Store {
	return programEventJoined(db, contents, nil).Store
}

// programEventLockActive is the identity fence of the translation domain's
// transaction.
func programEventLockActive(ctx context.Context, tx *gorm.DB) programevent.LockActive {
	return func(principal *auth.UserInfo) (bool, error) {
		return identitystate.LockActivePrincipal(ctx, tx, principal)
	}
}

// programEventAnswer gives a Program Event use-case rejection the Connect
// form the translation registry classifies by code; other errors pass
// unchanged.
func programEventAnswer(err error) error {
	if _, rejected := errors.AsType[*domainerr.Error](err); rejected {
		return errs.Wrap(err)
	}
	return err
}
