package page

import (
	"context"
	"encoding/json"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// Store is what the Page use cases read and write, bound to one transaction
// or read snapshot. pagepostgres implements it over the page tables and the
// shared content, media, OG, route, Member, Share Link and Menu runtimes a
// Page transaction drives; pagetest is the in-memory fake.
type Store interface {
	PageRows
	PageLocales
	PageVersions
	ContentDocuments
	PageMedia
	PageCollaboration
	// Audience is what the Page's access policy reads about the reader.
	pageaccess.Audience

	// DeleteShareLinks removes the Page's share links.
	DeleteShareLinks(ctx context.Context, pageID string) error
	// UpdateMenuTargets points the Menu links to the Page's previous slug
	// at next, its new slug or its ID.
	UpdateMenuTargets(ctx context.Context, pageID, previousSlug, next string) error
	// RemoveMenuTargets removes the Menu links to the Page.
	RemoveMenuTargets(ctx context.Context, pageID, slug string) error
	// PublishContentUpdate schedules the content.updated signal of update.
	PublishContentUpdate(ctx context.Context, update ContentUpdate) error
	// RuntimeSettings reads the localization settings, storing the
	// defaults when none exist.
	RuntimeSettings(ctx context.Context) (translation.RuntimeSettings, error)
}

// PageRows reads and writes the page table. A missing row is
// ErrPageNotFound.
type PageRows interface {
	// ListPages lists one page of Pages and counts every match.
	ListPages(ctx context.Context, page ListPage) ([]Page, int64, error)
	// FindPage reads the Page whose locator column equals value, with lock.
	FindPage(ctx context.Context, locator Locator, value string, lock RowLock) (Page, error)
	// DocumentRoot reads the Page's content document reference and source
	// locale with lock.
	DocumentRoot(ctx context.Context, pageID string, lock RowLock) (DocumentRoot, error)
	// AccessPolicy reads the Page's stored access policy.
	AccessPolicy(ctx context.Context, pageID string) (json.RawMessage, error)
	// DocumentLayout reads the Page's document layout.
	DocumentLayout(ctx context.Context, pageID string) (DocumentLayout, error)
	// LockSettings locks the Page row for update and reads its
	// configuration.
	LockSettings(ctx context.Context, pageID string) (Settings, error)
	// SlugTaken reports whether a Page other than excludeID holds slug.
	SlugTaken(ctx context.Context, slug, excludeID string) (bool, error)
	// RouteOccupied reports whether another resource's route uses slug,
	// and LockRoute locks the route of slug.
	RouteOccupied(ctx context.Context, slug string) (bool, error)
	LockRoute(ctx context.Context, slug string) error
	// InsertPage stores a new Page and fills its ID. A slug another Page
	// holds is ErrSlugTaken.
	InsertPage(ctx context.Context, page *Page) error
	// UpdateSettings stores change and updated_at. A slug another Page
	// holds is ErrSlugTaken.
	UpdateSettings(ctx context.Context, pageID string, change SettingsChange, now time.Time) error
	// SetStatus stores the lifecycle status, published_at when publishedAt
	// is set, and updated_at.
	SetStatus(ctx context.Context, pageID string, status Status, publishedAt *time.Time, now time.Time) error
	// SetFeaturedImage stores fileID, or clears it when nil, and updated_at.
	SetFeaturedImage(ctx context.Context, pageID string, fileID *string, now time.Time) error
	// SetDocumentLayout stores layout and updated_at.
	SetDocumentLayout(ctx context.Context, pageID string, layout DocumentLayout, now time.Time) error
	// SwitchSourceLocale moves the source locale from from to to and
	// reports whether from was still the source locale.
	SwitchSourceLocale(ctx context.Context, pageID, from, to string, now time.Time) (bool, error)
	// TouchPage moves updated_at.
	TouchPage(ctx context.Context, pageID string, now time.Time) error
	// DeletePage deletes the Page row.
	DeletePage(ctx context.Context, pageID string) error
}

// PageLocales reads and writes the page_translation table.
type PageLocales interface {
	// SourceMetadata reads the source-locale metadata of each Page of
	// pageIDs that has it.
	SourceMetadata(ctx context.Context, pageIDs []string) (map[string]SourceMetadata, error)
	// Locale reads one locale with lock and reports whether it exists.
	Locale(ctx context.Context, pageID, locale string, lock RowLock) (Locale, bool, error)
	// InsertLocale creates a locale row.
	InsertLocale(ctx context.Context, pageID, locale string, title, summary *string, now time.Time) error
	// CreateTargetLocale creates a target locale row and returns its
	// incarnation.
	CreateTargetLocale(ctx context.Context, pageID, locale string, title, summary *string, now time.Time) (uuid.UUID, error)
	// UpsertLocale stores the title and summary of a locale, creating it.
	UpsertLocale(ctx context.Context, pageID, locale string, title, summary *string, now time.Time) error
	// UpdateLocale writes change and reports whether the locale exists.
	UpdateLocale(ctx context.Context, pageID, locale string, change LocaleChange) (bool, error)
	// DeleteLocale deletes the locale and reports whether it existed.
	DeleteLocale(ctx context.Context, pageID, locale string) (bool, error)
}

// PageVersions reads and writes the page_version table.
type PageVersions interface {
	// ListVersions lists one page of the Page's versions, newest first, and
	// counts them.
	ListVersions(ctx context.Context, pageID string, limit, offset int32) ([]Version, int64, error)
	// FindVersion reads a version. A missing one is ErrVersionNotFound.
	FindVersion(ctx context.Context, versionID string) (Version, error)
	// LockLatestVersion locks the Page's newest version and reports
	// whether there is one.
	LockLatestVersion(ctx context.Context, pageID string) (Version, bool, error)
	// InsertVersion stores a new version and fills its ID.
	InsertVersion(ctx context.Context, version *Version) error
}

// ContentFence runs before the content store locks or changes the Page's
// document rows (contentblock.DomainFence): it locks the Page root in the
// same transaction, rechecks authority, and names the source locale.
type ContentFence func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error)

// MetadataEffect writes the Page metadata of a content write, in its
// transaction.
type MetadataEffect func(ctx context.Context, store Store) (contentblock.MetadataEffect, error)

// TargetMetadataEffect writes the Page locale row of a target-locale
// content batch, in its transaction.
type TargetMetadataEffect func(ctx context.Context, store Store, contentChanged bool) (contentblock.MetadataEffect, error)

// ContentDocuments is the Page's content document in the shared content
// store (contentblock.Store), bound to the Page transaction. Each fence runs
// before the content rows are locked.
type ContentDocuments interface {
	CreateContentDocument(ctx context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error)
	// LockedContentSnapshot reads the document in the transaction, after
	// the caller locked the Page root.
	LockedContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	// ContentSnapshot reads the document in one snapshot of its own.
	ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence ContentFence) (contentblock.Result, error)
	ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence ContentFence, effect MetadataEffect) (contentblock.Result, error)
	ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence ContentFence, effect TargetMetadataEffect) (contentblock.Result, error)
	ReplaceContentSnapshot(ctx context.Context, replace contentblock.ReplaceInput, fence ContentFence) (contentblock.Result, error)
	AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence ContentFence, effect MetadataEffect) (contentblock.AdvanceResult, error)
	DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence ContentFence) error
	// LocaleOwnershipConflict locks the document and answers a save whose
	// room no longer owns its locale with the document's revision.
	LocaleOwnershipConflict(ctx context.Context, documentID uuid.UUID, message string) error
	// PublicationAttachments lists the File attachments publication needs
	// ready.
	PublicationAttachments(ctx context.Context, documentID uuid.UUID) ([]contentblock.PublicationAttachment, error)
	// MediaReferences lists the document's Block media.
	MediaReferences(ctx context.Context, documentID uuid.UUID) ([]*contentv1.ContentBlockMediaItem, error)
}

// PageMedia is the shared File, public asset and OG lifecycle of a Page.
type PageMedia interface {
	// LockAttachableFiles locks Files a Page may attach.
	LockAttachableFiles(ctx context.Context, fileIDs []string) error
	// FileExists requires the File; a missing one is ErrFileNotFound.
	FileExists(ctx context.Context, fileID string) error
	// PublicationFiles share-locks and reads the Files of fileIDs, ordered
	// by ID.
	PublicationFiles(ctx context.Context, fileIDs []string) ([]PublicationFile, error)
	// PublicationPresentations reads which presentations of fileIDs are
	// ready.
	PublicationPresentations(ctx context.Context, fileIDs []string) (Presentations, error)
	// ReleaseFeaturedImageBindings releases the bindings of the featured
	// image, and ReleaseExactFeaturedImageBinding the featured image slot.
	ReleaseFeaturedImageBindings(ctx context.Context, pageID string) error
	ReleaseExactFeaturedImageBinding(ctx context.Context, pageID string) error
	// ReadyAssets resolves ready public assets by ID.
	ReadyAssets(ctx context.Context, assetIDs []string) (map[string]*mediaasset.Reference, error)
	// UnavailableAttachments names the attachments of a version document
	// whose media is gone.
	UnavailableAttachments(ctx context.Context, document *contentv1.LocalizedPageDocument) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error)
	// HydrateBlockMedia adds the delivery principal may receive to the
	// Page's Block media.
	HydrateBlockMedia(ctx context.Context, pageID string, documentID uuid.UUID, principal *auth.UserInfo, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error)
	// RequestOG starts the OG generation of locale, or of every locale,
	// and returns its run ID, empty when none started; CancelOG cancels it
	// and releases its image.
	RequestOG(ctx context.Context, pageID, locale string, allLocales bool, reason string) (string, error)
	CancelOG(ctx context.Context, pageID string) error
}

// PageCollaboration is the shared session, Member and contributor state a
// Page save and its collaboration room check in their transaction.
type PageCollaboration interface {
	// ResolveSessionPrincipal resolves a browser session's principal; an
	// invalid session is auth.ErrSessionPrincipalInvalid.
	ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error)
	// LockSession locks the session of principal; a session no longer
	// valid is auth.ErrSessionPrincipalInvalid.
	LockSession(ctx context.Context, sessionID string, principal *auth.UserInfo) error
	// LockMembers takes a KEY SHARE lock on each existing Member of
	// memberIDs and returns the IDs it locked.
	LockMembers(ctx context.Context, memberIDs []string) ([]string, error)
	// ActiveIdentities returns the linked Identity of each onboarded, live
	// Member of memberIDs whose Identity is active and not banned.
	ActiveIdentities(ctx context.Context, memberIDs []string) (map[string]string, error)
	// Nicknames returns the nickname of each existing Member of memberIDs.
	Nicknames(ctx context.Context, memberIDs []string) (map[string]string, error)
	// PreferredLocale returns the preferred locale of a live Member, or nil.
	PreferredLocale(ctx context.Context, memberID string) (*string, error)
	// LockUserTags takes a SHARE lock on each existing user tag of tagIDs
	// and returns the IDs it locked.
	LockUserTags(ctx context.Context, tagIDs []string) ([]string, error)
}

// PublicationFile is a File a Page Block attaches.
type PublicationFile struct {
	ID              string
	MIMEType        string
	DurationSeconds *int
	DeleteRequested bool
}

// Presentations is which presentations of Files are ready: source assets
// by File ID and asset kind, derivatives by File ID and derivative type.
type Presentations struct {
	SourceAssets map[PresentationKey]bool
	Derivatives  map[PresentationKey]bool
}

// PresentationKey names one presentation of a File: an asset kind ("image",
// "mesh") or a derivative type (the stored FILE_DERIVATIVE_TYPE_* value).
type PresentationKey struct {
	FileID string
	Kind   string
}

// Joined is a transaction a pipeline outside Page opened, which a Page step
// joins instead of opening its own: a translation delivery or interchange,
// or another domain's relation policy. Store and Audit are both bound to
// that transaction, so the step passes a nil transaction to domainaudit.
type Joined struct {
	Store Store
	Audit domainaudit.Appender
}
