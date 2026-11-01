package form

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// Store is what the Form use cases read and write, bound to one transaction
// or read snapshot. formpostgres implements it over the form tables and the
// shared content, media, OG, route, collaboration, Member and Share Link
// runtimes a Form transaction drives; formtest is the in-memory fake.
type Store interface {
	FormRows
	FormLocales
	FormSubmissions
	ContentDocuments
	FormMedia

	// RequireCurrentContributor re-resolves the one origin Member a
	// collaboration save is attributed to.
	RequireCurrentContributor(ctx context.Context, formID, memberID string) error
	// PreferredLocale returns the preferred locale of a live Member, or nil.
	PreferredLocale(ctx context.Context, memberID string) (*string, error)
	// DeleteShareLinks removes the Form's share links.
	DeleteShareLinks(ctx context.Context, formID string) error
	// PublishContentUpdate schedules the content.updated signal of update.
	PublishContentUpdate(ctx context.Context, update ContentUpdate) error
	// RuntimeSettings reads the localization settings, storing the
	// defaults when none exist.
	RuntimeSettings(ctx context.Context) (translation.RuntimeSettings, error)
}

// FormRows reads and writes the form table. A missing row is
// ErrFormNotFound.
type FormRows interface {
	// FindForm reads the Form whose locator column equals value.
	FindForm(ctx context.Context, locator Locator, value string) (Form, error)
	// FormExists reports whether the Form exists.
	FormExists(ctx context.Context, formID string) (bool, error)
	// LockForm locks the Form row for update and reads it.
	LockForm(ctx context.Context, formID string) (Form, error)
	// LockFormRoot locks the Form row for update and returns its stored ID.
	LockFormRoot(ctx context.Context, formID string) (string, error)
	// LockFeaturedImage locks the Form row for update and reads its
	// featured image File.
	LockFeaturedImage(ctx context.Context, formID string) (*string, error)
	// DocumentRoot reads the Form's content document reference, source
	// locale and status with lock.
	DocumentRoot(ctx context.Context, formID string, lock RowLock) (DocumentRoot, error)
	// SourceLocale reads the Form's source locale.
	SourceLocale(ctx context.Context, formID string) (string, error)
	// ListForms lists one page of Forms and counts every match.
	ListForms(ctx context.Context, list ListForms) ([]Form, int64, error)
	// SourceTitles reads the source title of each Form of formIDs, the
	// default title when it has none.
	SourceTitles(ctx context.Context, formIDs []string) (map[string]string, error)
	// SlugTaken reports whether a Form other than excludeID holds slug.
	SlugTaken(ctx context.Context, slug, excludeID string) (bool, error)
	// RouteAvailable reports whether no other resource's route uses slug,
	// and LockRoute locks the route of slug, failing when it is taken.
	RouteAvailable(ctx context.Context, slug string) (bool, error)
	LockRoute(ctx context.Context, slug string) error
	// InsertForm stores a new Form and fills it with the stored row.
	InsertForm(ctx context.Context, form *Form) error
	// UpdateSettings stores the settings of next and updated_at.
	UpdateSettings(ctx context.Context, next Form, settings []Setting, now time.Time) error
	// SetFeaturedImage stores fileID, or clears it when nil, and updated_at.
	SetFeaturedImage(ctx context.Context, formID string, fileID *string, now time.Time) error
	// TouchForm moves updated_at.
	TouchForm(ctx context.Context, formID string, now time.Time) error
	// DeleteForm deletes the Form row.
	DeleteForm(ctx context.Context, formID string) error
}

// FormLocales reads and writes the form_translation table.
type FormLocales interface {
	// Locale reads one locale, locked for update when update is set, and
	// reports whether it exists.
	Locale(ctx context.Context, formID, locale string, update bool) (Locale, bool, error)
	// LocaleState reads one locale with its OG image. A missing locale is
	// ErrLocaleNotFound.
	LocaleState(ctx context.Context, formID, locale string) (TranslationDocumentState, error)
	// LocalizationRows reads the Form's locales of locales, every locale
	// when it is empty.
	LocalizationRows(ctx context.Context, formID string, locales []string) ([]LocalizationRow, error)
	// SaveSourceLocale stores the source locale document, creating it; a
	// nil title keeps the stored one. The schema is required.
	SaveSourceLocale(ctx context.Context, formID, locale string, title *string, schema []byte, contentText *string, now time.Time) error
	// CreateEmptyLocale creates a locale without title and text over
	// schema, and reports false when the locale exists.
	CreateEmptyLocale(ctx context.Context, formID, locale string, schema []byte, now time.Time) (bool, error)
	// InsertLocale creates a locale from change.
	InsertLocale(ctx context.Context, formID, locale string, change LocaleChange, createdAt time.Time) error
	// UpdateLocale writes change and reports whether the locale exists.
	UpdateLocale(ctx context.Context, formID, locale string, change LocaleChange) (bool, error)
	// DeleteLocale deletes the locale and reports whether it existed.
	DeleteLocale(ctx context.Context, formID, locale string) (bool, error)
}

// FormSubmissions reads and writes the form_submission table.
type FormSubmissions interface {
	// ListSubmissions lists one page of a Form's submissions and counts
	// every match.
	ListSubmissions(ctx context.Context, list SubmissionList) ([]Submission, int64, error)
	// SubmissionCounts counts the submissions of each Form of formIDs that
	// has any.
	SubmissionCounts(ctx context.Context, formIDs []string) (map[string]int64, error)
	// SubmissionData reads the creation time and answers of every
	// submission of the Form, newest first.
	SubmissionData(ctx context.Context, formID string) ([]SubmissionData, error)
	// FindSubmission reads a submission. A missing one is
	// ErrSubmissionNotFound.
	FindSubmission(ctx context.Context, submissionID string) (Submission, error)
	// DeleteSubmission deletes a submission and reports whether it
	// existed.
	DeleteSubmission(ctx context.Context, submissionID string) (bool, error)
}

// ContentFence runs before the content store locks the Form's content
// document (contentblock.DomainFence) and names its source locale.
type ContentFence func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error)

// MetadataEffect writes the Form locale rows of a revision advance, in its
// transaction.
type MetadataEffect func(ctx context.Context, store Store) (contentblock.MetadataEffect, error)

// ContentDocuments is the Form's content document in the shared content
// store (contentblock.Store), bound to the Form transaction.
type ContentDocuments interface {
	// CreateContentDocument creates the document and returns its ID.
	CreateContentDocument(ctx context.Context, input contentblock.CreateInput) (uuid.UUID, error)
	// DocumentRevision reads the document's current revision.
	DocumentRevision(ctx context.Context, documentID uuid.UUID) (uuid.UUID, error)
	// AdvanceContentRevision runs effect as the next revision of the
	// document when input's revision is current; a stale revision is a
	// *contentblock.StaleRevisionError.
	AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence ContentFence, effect MetadataEffect) (contentblock.AdvanceResult, error)
}

// FormMedia is the shared File, public asset and OG lifecycle of a Form.
type FormMedia interface {
	// LockAttachableFiles locks Files a Form may attach.
	LockAttachableFiles(ctx context.Context, fileIDs []string) error
	// FileID reads the stored ID of the File; a missing one is
	// ErrFileNotFound.
	FileID(ctx context.Context, fileID string) (string, error)
	// BindFeaturedImage binds the File's ready image as the Form's
	// featured image, and ReleaseFeaturedImage releases it.
	BindFeaturedImage(ctx context.Context, formID, fileID string) (*Asset, error)
	ReleaseFeaturedImage(ctx context.Context, formID string) error
	// FeaturedImage resolves the Form's ready featured image; a failure is
	// logged and answers none.
	FeaturedImage(ctx context.Context, formID string) *Asset
	// ReadyOGAsset resolves the first ready public asset of localizedID
	// and sourceID.
	ReadyOGAsset(ctx context.Context, localizedID, sourceID *string) (*Asset, error)
	// RequestOG starts the OG generation of locale, or of every locale,
	// and returns its run ID, empty when none started; CancelOG cancels
	// every generation and releases the OG image.
	RequestOG(ctx context.Context, formID, locale string, allLocales bool, reason string) (string, error)
	CancelOG(ctx context.Context, formID string) error
}

// Joined is a transaction a pipeline outside Form opened, which a Form step
// joins instead of opening its own: a translation delivery or interchange.
// Store and Audit are both bound to that transaction, so the step passes a
// nil transaction to domainaudit.
type Joined struct {
	Store Store
	Audit domainaudit.Appender
}
