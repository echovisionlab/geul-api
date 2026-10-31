// Package contentsignal atomically persists content.updated in PGMQ and schedules
// its live NOTIFY wake-up in the owning domain transaction. Offline listeners
// can recover the durable envelope; delivery consumers must deduplicate by ID.
package contentsignal

import (
	"context"
	"errors"
	"fmt"
	"strings"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// Publisher is the transactional signal capability of the PostgreSQL
// publisher. The executor is the caller's live transaction.
type Publisher interface {
	NotifyProtobufWithExecutor(context.Context, eventpkg.DBTX, string, proto.Message) error
}

// ErrPublisherRequired is returned when a domain mutation that must signal
// content.updated has no transactional publisher.
var ErrPublisherRequired = errors.New("content.updated transactional publisher is required")

// PublishInTransaction validates event and schedules it as one content.updated
// NOTIFY in tx. A nil event means the mutation produced no observable content
// change and is a no-op.
func PublishInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	publisher Publisher,
	event *managev1.ContentUpdatedEvent,
) error {
	if event == nil {
		return nil
	}
	if publisher == nil {
		return ErrPublisherRequired
	}
	// Central stamping covers every producer. Collab persistence uses its own
	// acknowledgements and must never carry a browser write origin.
	event.Origin = nil
	if event.GetSource() != managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB {
		if origin := ClientOrigin(ctx); origin != "" {
			event.Origin = &origin
		}
	}
	if err := completeRevision(ctx, tx, event); err != nil {
		return err
	}
	if err := Validate(event); err != nil {
		return err
	}
	executor, err := TransactionExecutor(tx)
	if err != nil {
		return err
	}
	if err := publisher.NotifyProtobufWithExecutor(ctx, executor, eventpkg.SignalContentUpdated, event); err != nil {
		return fmt.Errorf("schedule content.updated for %s %s: %w", event.GetEntityType(), event.GetEntityId(), err)
	}
	return nil
}

// TransactionExecutor returns the SQL executor owned by a live GORM
// transaction, for PGMQ commands and PostgreSQL signals that must commit with
// the domain state they describe. It refuses a non-transactional pool, which
// would send them even when the mutation rolls back.
func TransactionExecutor(tx *gorm.DB) (eventpkg.DBTX, error) {
	if tx == nil || tx.Statement == nil || tx.Statement.ConnPool == nil {
		return nil, errors.New("PGMQ work requires the owning database transaction")
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return nil, errors.New("PGMQ work must be scheduled inside the owning database transaction")
	}
	executor, ok := tx.Statement.ConnPool.(eventpkg.DBTX)
	if !ok {
		return nil, errors.New("database transaction does not expose a PGMQ executor")
	}
	return executor, nil
}

// Validate enforces the producer-independent content.updated invariants that
// downstream consumers (editor-collab, caches, translation) rely on: an
// explicit source, a resulting document revision for every document state or
// locale-owned change, and a complete locale tuple.
func Validate(event *managev1.ContentUpdatedEvent) error {
	if event == nil {
		return errors.New("content.updated event is required")
	}
	if event.GetEntityType() == managev1.ContentEntityType_CONTENT_ENTITY_TYPE_UNSPECIFIED {
		return errors.New("content.updated entity_type is required")
	}
	if strings.TrimSpace(event.GetEntityId()) == "" {
		return errors.New("content.updated entity_id is required")
	}
	if event.GetSource() == managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_UNSPECIFIED {
		return errors.New("content.updated source is required")
	}
	revisionRequired := true
	if event.DocumentRevision != nil && strings.TrimSpace(event.GetDocumentRevision()) == "" {
		return errors.New("content.updated document_revision must not be blank")
	}
	if revisionRequired && event.DocumentRevision == nil {
		return errors.New("content.updated document_revision is required for document and locale changes")
	}
	if event.Locale != nil {
		if strings.TrimSpace(event.GetLocale()) == "" {
			return errors.New("content.updated locale must not be blank")
		}
		if event.LocaleExists == nil {
			return errors.New("content.updated locale_exists is required with locale")
		}
	} else if event.LocaleExists != nil || event.TargetRevision != nil {
		return errors.New("content.updated locale tuple requires locale")
	}
	if event.TargetRevision != nil {
		if strings.TrimSpace(event.GetTargetRevision()) == "" {
			return errors.New("content.updated target_revision must not be blank")
		}
		if !event.GetLocaleExists() || event.GetDocumentStateChanged() {
			return errors.New("content.updated target_revision requires an existing target-only change")
		}
	}
	return nil
}

// Metadata and media writers retain the document token even when they do not
// advance it. The caller already owns the resource lock.
func completeRevision(ctx context.Context, tx *gorm.DB, event *managev1.ContentUpdatedEvent) error {
	if event.DocumentRevision != nil {
		return nil
	}
	tables := map[managev1.ContentEntityType]string{
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST:           "post",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE:           "page",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK:           "work",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_ARTIST:         "artist",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_LABEL:          "label",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_RELEASE:        "release",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_FORM:           "form",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PROGRAM_EVENT:  "program_event",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST_SERIES:    "series",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_MENU:           "menu",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_EMAIL_TEMPLATE: "email_template",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_EMAIL_LAYOUT:   "email_layout",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_CAMPAIGN:       "campaign",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PRIVACY:        "privacy_history",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_TERMS:          "terms_history",
	}
	table, ok := tables[event.EntityType]
	if !ok {
		return errors.New("unknown content.updated entity")
	}
	if _, err := TransactionExecutor(tx); err != nil {
		return err
	}
	var row struct{ Revision string }
	if err := tx.WithContext(ctx).Table(table+" AS owner").
		Select("document.revision").Joins("JOIN content_document AS document ON document.id = owner.content_document_id").
		Where("owner.id = ?", event.EntityId).Take(&row).Error; err != nil {
		return err
	}
	event.DocumentRevision = &row.Revision
	return nil
}

// LocaleChange is one change to one locale of a collaborative content
// document: the input every domain's locale ContentUpdatedEvent builder
// takes. The builder decides which combinations it publishes.
type LocaleChange struct {
	EntityID string
	// Fields names what changed, such as "title", "summary" or "content";
	// each domain maps its names to event paths.
	Fields           []string
	DocumentRevision string
	Contributors     []string
	Source           managev1.ContentUpdateSource
	Locale           string
	// LocaleDeleted marks a change that removed the target locale.
	LocaleDeleted bool
	// TargetRevision is the new target revision of a target-locale change;
	// nil for a source change or a deleted locale.
	TargetRevision *string
	// DocumentStateChanged is set when the shared document revision
	// advanced, which only a source-locale change does.
	DocumentStateChanged bool
}
