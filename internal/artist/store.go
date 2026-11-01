package artist

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// Store is what the Artist use cases read and write, bound to one
// transaction or read snapshot. artistpostgres implements it over the
// artist tables and the shared content, media, OG, route and relation
// runtimes the Artist transaction drives; artisttest is the in-memory fake.
type Store interface {
	entityparticipant.Store
	ArtistRows
	ArtistLocales
	ContentDocuments
	ArtistMedia
	ArtistCollaboration
	ArtistRelations

	// PublishContentUpdate schedules the content.updated signal of update.
	PublishContentUpdate(ctx context.Context, update ContentUpdate) error
	// AppendMemberAudit appends a record attributed to memberID through
	// writer, for a delivery that runs in another domain's transaction.
	AppendMemberAudit(ctx context.Context, writer domainaudit.Appender, memberID string, action sharedtelemetry.AuditAction, build domainaudit.Builder) error
}

// ListPage is the page of Artists a Store lists: only published Artists
// when PublishedOnly is set, only IDs when IDs is not nil, filtered (a
// label_id filter keeps the Artists linked to that Label), sorted, ties
// broken by ID when TieByID is set, and paged.
type ListPage struct {
	PublishedOnly bool
	IDs           []string
	Filters       []*commonv1.FilterSpec
	Sorts         []*commonv1.SortSpec
	TieByID       bool
	Limit         int32
	Offset        int32
}

// ArtistRows reads and writes the artist table and the Artist's Label
// links. A missing row is ErrArtistNotFound.
type ArtistRows interface {
	// FindArtist reads the Artist row.
	FindArtist(ctx context.Context, artistID string) (Artist, error)
	// ShareLockArtist share-locks the Artist row and reads it.
	ShareLockArtist(ctx context.Context, artistID string) (Artist, error)
	// LockArtistID locks the Artist row for update.
	LockArtistID(ctx context.Context, artistID string) (string, error)
	// ListArtists lists one page of Artists and counts every match.
	ListArtists(ctx context.Context, page ListPage) ([]Artist, int64, error)
	// SlugTaken reports whether an Artist other than excludeID holds slug.
	SlugTaken(ctx context.Context, slug, excludeID string) (bool, error)
	// InsertArtist stores a new Artist and fills its ID and created_at.
	InsertArtist(ctx context.Context, artist *Artist) error
	// InsertCreatorOwner makes memberID the first Owner of a new Artist.
	InsertCreatorOwner(ctx context.Context, artistID, memberID string, now time.Time) error
	// SetArtistStatus stores the lifecycle status, published_at when
	// publishedAt is set, and updated_at.
	SetArtistStatus(ctx context.Context, artistID string, status Status, publishedAt *time.Time, now time.Time) error
	// TouchArtist moves updated_at; StampArtist moves it without checking
	// the row.
	TouchArtist(ctx context.Context, artistID string, now time.Time) error
	StampArtist(ctx context.Context, artistID string, now time.Time) error
	// DeleteArtist deletes the Artist row.
	DeleteArtist(ctx context.Context, artistID string) error
	// ContentDocumentID reads the Artist's content document reference with
	// lock; nil when it is not set.
	ContentDocumentID(ctx context.Context, artistID string, lock RowLock) (*string, error)
	// SourceLocale reads the Artist's source locale with lock.
	SourceLocale(ctx context.Context, artistID string, lock RowLock) (string, error)
	// DocumentRoot reads the Artist root of a content mutation with lock.
	DocumentRoot(ctx context.Context, artistID string, lock RowLock) (DocumentRoot, error)
	// DocumentMetadata reads the metadata a collaboration room edits.
	DocumentMetadata(ctx context.Context, artistID string) (DocumentMetadata, error)
	// UpdateDocumentMetadata stores the changed metadata columns and
	// updated_at.
	UpdateDocumentMetadata(ctx context.Context, artistID string, change MetadataChange, now time.Time) error
	// LabelIDs lists the Artist's Labels by ID; LabelIDsInLinkOrder in the
	// order the Artist links them.
	LabelIDs(ctx context.Context, artistID string) ([]string, error)
	LabelIDsInLinkOrder(ctx context.Context, artistID string) ([]string, error)
	// ReplaceLabelIDs replaces the Artist's Labels, in order.
	ReplaceLabelIDs(ctx context.Context, artistID string, labelIDs []string) error
	// CheckResourceRoute requires that no route uses the Artist slug, and
	// LockResourceRoute locks the route first.
	CheckResourceRoute(ctx context.Context, slug string) error
	LockResourceRoute(ctx context.Context, slug string) error
	// ResourceRouteAvailable reports whether no route uses the Artist slug.
	ResourceRouteAvailable(ctx context.Context, slug string) (bool, error)
}

// ArtistLocales reads and writes the artist_translation table.
type ArtistLocales interface {
	// LocaleTitle reads one locale, locked for update when forUpdate is
	// set, and reports whether it exists.
	LocaleTitle(ctx context.Context, artistID, locale string, forUpdate bool) (LocaleTitle, bool, error)
	// LocaleTitleText reads one locale's title. A missing locale is
	// ErrLocaleNotFound.
	LocaleTitleText(ctx context.Context, artistID, locale string) (string, error)
	// LocaleTitleValue reads one locale's title, which may be NULL, and
	// reports whether the locale exists.
	LocaleTitleValue(ctx context.Context, artistID, locale string) (*string, bool, error)
	// LocaleExists reports whether the locale row exists.
	LocaleExists(ctx context.Context, artistID, locale string) (bool, error)
	// LocaleOgAssetID reads one locale's OG image. A missing locale is
	// ErrLocaleNotFound.
	LocaleOgAssetID(ctx context.Context, artistID, locale string) (*string, error)
	// SaveLocaleTitle stores the locale's title, creating the locale.
	SaveLocaleTitle(ctx context.Context, artistID, locale, title string, now time.Time) error
	// CreateTargetLocale creates an untitled target locale and reports
	// whether it did; EnsureTargetLocale leaves an existing one alone.
	CreateTargetLocale(ctx context.Context, artistID, locale string, now time.Time) (bool, error)
	EnsureTargetLocale(ctx context.Context, artistID, locale string) (bool, error)
	// TouchTargetLocale moves the locale's updated_at and reports whether
	// it exists.
	TouchTargetLocale(ctx context.Context, artistID, locale string, updatedAt time.Time) (bool, error)
	// SetTargetLocaleTitle stores the locale's title and updated_at and
	// reports whether it exists.
	SetTargetLocaleTitle(ctx context.Context, artistID, locale, title string, updatedAt time.Time) (bool, error)
	// DeleteTargetLocale deletes the locale and reports whether it existed.
	DeleteTargetLocale(ctx context.Context, artistID, locale string) (bool, error)
}

// TargetMetadataEffect writes the Artist locale row of a target-locale
// content batch, in its transaction.
type TargetMetadataEffect func(ctx context.Context, store Store, contentChanged bool) (contentblock.MetadataEffect, error)

// MetadataEffect writes the Artist metadata of a revision advance, in its
// transaction.
type MetadataEffect func(ctx context.Context, store Store) (contentblock.MetadataEffect, error)

// ContentFence runs before the content store locks or changes the Artist's
// document rows (contentblock.DomainFence).
type ContentFence func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error)

// ContentDocuments is the Artist's content document in the shared content
// store (contentblock.Store), bound to the Artist transaction.
type ContentDocuments interface {
	CreateContentDocument(ctx context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error)
	// LockedContentSnapshot share-locks and reads the document in the
	// transaction; ContentSnapshot reads it in one snapshot of its own.
	LockedContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	// CurrentContentRevision reads the document's revision; a missing
	// document row is ErrDocumentRowNotFound.
	CurrentContentRevision(ctx context.Context, documentID uuid.UUID) (uuid.UUID, error)
	// LocaleOwnershipConflict is the conflict of a room whose source locale
	// moved (contentblock.LocaleOwnershipConflict).
	LocaleOwnershipConflict(ctx context.Context, documentID uuid.UUID, message string) error
	ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence ContentFence) (contentblock.Result, error)
	ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence ContentFence, effect TargetMetadataEffect) (contentblock.Result, error)
	ReplaceContentSnapshot(ctx context.Context, replace contentblock.ReplaceInput, fence ContentFence) (contentblock.Result, error)
	AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence ContentFence, effect MetadataEffect) (contentblock.AdvanceResult, error)
	DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence ContentFence) error
}

// ArtistMedia is the shared File, public asset and OG lifecycle of the
// Artist's images.
type ArtistMedia interface {
	// Images lists each Artist's ordered images with their ready assets.
	Images(ctx context.Context, artistIDs []string) (map[string][]ArtistImageProjection, error)
	// ReplaceImageBindings binds fileIDs, in order, as the Artist's images;
	// ReleaseImageBindings releases every image binding.
	ReplaceImageBindings(ctx context.Context, artistID string, fileIDs []string) error
	ReleaseImageBindings(ctx context.Context, artistID string) error
	// ReadyAssets resolves ready public assets by ID.
	ReadyAssets(ctx context.Context, assetIDs []string) (map[string]*commonv1.AssetRef, error)
	// RequestOG starts the Artist's current OG generation for locale and
	// returns its run ID, empty when none started; CancelOG cancels it and
	// releases its image.
	RequestOG(ctx context.Context, artistID, locale string, sourceChanged bool, reason string) (string, error)
	CancelOG(ctx context.Context, artistID string) error
}

// ArtistCollaboration is the shared collaboration and translation authority
// an Artist save checks in its transaction.
type ArtistCollaboration interface {
	// ResolveSessionPrincipal resolves a collaboration session's principal;
	// an invalid session is auth.ErrSessionPrincipalInvalid.
	ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error)
	// RequireCurrentContributors requires that the contributors may still
	// save the Artist's room.
	RequireCurrentContributors(ctx context.Context, artistID string, contributors []string) error
	// RequireDocumentContributors validates and key-share-locks the
	// contributor Members of a save.
	RequireDocumentContributors(ctx context.Context, contributors []string) error
	// ResolveInitialSourceLocale picks a new Artist's source locale.
	ResolveInitialSourceLocale(ctx context.Context, acceptLanguage string) string
}

// ArtistRelations reads the rows of other domains that reference an Artist,
// each with its display name and relation count.
type ArtistRelations interface {
	ChildArtists(ctx context.Context, artistID string) ([]entityparticipant.Related, error)
	ProgramEventsOfArtist(ctx context.Context, artistID string) ([]entityparticipant.Related, error)
	ReleasesOfArtist(ctx context.Context, artistID string) ([]entityparticipant.Related, error)
	TracksOfArtist(ctx context.Context, artistID string) ([]entityparticipant.Related, error)
	WorksOfArtist(ctx context.Context, artistID string) ([]entityparticipant.Related, error)
	// LabelTitles maps each existing Label of labelIDs to its source title;
	// DraftLabelIDs reports which are draft Labels.
	LabelTitles(ctx context.Context, labelIDs []string) (map[string]string, error)
	DraftLabelIDs(ctx context.Context, labelIDs []string) (map[string]bool, error)
	// CountReleasesByArtist counts each Artist's Releases.
	CountReleasesByArtist(ctx context.Context, artistIDs []string) (map[string]int32, error)
	// DeleteShareLinks removes the Artist's share links.
	DeleteShareLinks(ctx context.Context, artistID string) error
}

// MetadataChange is what an Artist document-metadata save changes of its
// row.
type MetadataChange struct {
	entityparticipant.MetadataChange
	RealName entityparticipant.Assignment
}

// Empty reports whether the change writes no column.
func (c MetadataChange) Empty() bool {
	return !c.Slug.Changed && !c.CountryCode.Changed && !c.Website.Changed && !c.Parent.Changed &&
		!c.SetSocialLinks && !c.RealName.Changed
}
