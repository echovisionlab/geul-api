package emailcontent

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
)

// RowLock is the row lock a read takes.
type RowLock uint8

const (
	// Unlocked reads without a row lock.
	Unlocked RowLock = iota
	// ShareLock blocks a concurrent update.
	ShareLock
	// UpdateLock serializes with a concurrent writer.
	UpdateLock
)

// Fence runs before the content store locks or changes the owner's
// document rows, in the same transaction: it locks the owner's root, checks
// what the owner's lifecycle forbids and names the translation source
// locale (contentblock.DomainFence).
type Fence func(ctx context.Context, documentID uuid.UUID) (contentblock.DomainContext, error)

// MetadataEffect writes the owner's metadata of a content write in its
// transaction.
type MetadataEffect func(ctx context.Context) (contentblock.MetadataEffect, error)

// TargetMetadataEffect writes the target locale row of a target-locale
// content batch in its transaction.
type TargetMetadataEffect func(ctx context.Context, contentChanged bool) (contentblock.MetadataEffect, error)

// Store is an owner's email content bound to one transaction or read
// snapshot: its content document in the shared content store, the locale
// rows beside it (the owner's Entity+"_translation" table) and the
// content.updated signal of a write. emailcontentpostgres implements it;
// emailcontenttest is the in-memory fake.
type Store interface {
	ContentDocuments
	LocaleRows
	// PublishContentUpdate schedules the content.updated signal of an AI
	// document write; an unchanged write or a request without a signal
	// publisher schedules nothing.
	PublishContentUpdate(ctx context.Context, update ContentUpdate) error
}

// ContentDocuments is an owner's content document in the shared content
// store (contentblock.Store), bound to the owner's transaction. Each fence
// runs before the document rows are locked.
type ContentDocuments interface {
	CreateContentDocument(ctx context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error)
	// ContentSnapshot reads the document in one snapshot of its own.
	ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	// LockedContentSnapshot share-locks and reads the document in the
	// transaction, after the caller locked the owner's root.
	LockedContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence Fence) (contentblock.Result, error)
	ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence Fence, effect MetadataEffect) (contentblock.Result, error)
	ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence Fence, effect TargetMetadataEffect) (contentblock.Result, error)
	AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence Fence, effect MetadataEffect) (contentblock.AdvanceResult, error)
	DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence Fence) error
}

// LocaleRows reads and writes the owner's locale rows and touches the
// owner's root row.
type LocaleRows interface {
	// LocaleRow reads id's row of locale with lock and reports whether it
	// exists.
	LocaleRow(ctx context.Context, id, locale string, lock RowLock) (LocaleMetadata, bool, error)
	// LocaleNames lists the locales id has rows for, in order.
	LocaleNames(ctx context.Context, id string) ([]string, error)
	// InsertLocale creates the row of locale and reports whether it
	// inserted one row.
	InsertLocale(ctx context.Context, id, locale string, subject *string, now time.Time) (bool, error)
	// UpdateLocale stores the subject and updated_at of an existing row and
	// reports whether one row changed.
	UpdateLocale(ctx context.Context, id, locale string, subject *string, updatedAt time.Time) (bool, error)
	// UpsertLocaleSubject stores the subject of locale, creating its row.
	UpsertLocaleSubject(ctx context.Context, id, locale string, subject *string, now time.Time) error
	// DeleteLocale deletes the row of locale and reports whether it existed.
	DeleteLocale(ctx context.Context, id, locale string) (bool, error)
	// ProjectLocale stores the materialized content of locale, and
	// updated_at when projection sets it, and reports whether the row
	// exists.
	ProjectLocale(ctx context.Context, id, locale string, projection LocaleProjection) (bool, error)
	// TouchOwner moves the owner row's updated_at.
	TouchOwner(ctx context.Context, id string, now time.Time) error
}

// LocaleMetadata is one locale row of an owner; a row may hold no subject.
type LocaleMetadata struct {
	Locale    string
	Subject   *string
	UpdatedAt time.Time
}

// LocaleProjection is a locale's materialized content.
type LocaleProjection struct {
	HTML string
	Text string
	// UpdatedAt, when set, also moves the row's updated_at.
	UpdatedAt *time.Time
}

// ContentUpdate is the content.updated signal of an AI document write of
// Entity's id in Locale.
type ContentUpdate struct {
	Entity              string
	ID                  string
	Locale              string
	DocumentRevision    string
	TargetRevision      *string
	Changed             bool
	Deleted             bool
	ContributorMemberID string
}

// TranslationJob is the accepted translation job a typed translation
// delivery applies.
type TranslationJob struct {
	EntityType          string
	EntityID            string
	SourceLocale        string
	TargetLocale        string
	RequestedByMemberID string
}
