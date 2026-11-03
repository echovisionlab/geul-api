package release

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/google/uuid"
)

// Store is what the Release and Track use cases read and write, bound to
// one transaction or read snapshot. releasepostgres implements it over the
// Release tables and the shared content, media, OG, signal and
// collaboration runtimes a Release transaction drives; releasetest is the
// in-memory fake.
type Store interface {
	ReleaseRows
	ReleaseLocales
	ReleaseRelations
	TrackRows
	ContentDocuments
	ReleaseMedia
	ReleaseCollaboration
}

// ReleaseRows reads and writes the release table. A missing row is
// ErrReleaseNotFound.
type ReleaseRows interface {
	// FindRelease reads the Release row.
	FindRelease(ctx context.Context, releaseID string) (Release, error)
	// LockRelease reads the Release row with lock.
	LockRelease(ctx context.Context, releaseID string, lock RowLock) (Release, error)
	// ReleaseExists reads only the Release's ID, to answer a missing
	// Release.
	ReleaseExists(ctx context.Context, releaseID string) error
	// ListReleases lists one page of Releases and counts every match.
	ListReleases(ctx context.Context, page ListPage) ([]Release, int64, error)
	// SlugTaken reports whether a Release other than excludeID, when set,
	// holds slug.
	SlugTaken(ctx context.Context, slug string, excludeID *string) (bool, error)
	// InsertRelease stores a new Release and fills its ID and timestamps. A
	// slug another Release holds is ErrSlugTaken.
	InsertRelease(ctx context.Context, release *Release) error
	// UpdateReleaseSettings stores the settings and updated_at. A slug
	// another Release holds is ErrSlugTaken.
	UpdateReleaseSettings(ctx context.Context, releaseID string, settings Settings, now time.Time) error
	// SetReleaseStatus stores the lifecycle status, published_at when
	// publishedAt is set, and updated_at.
	SetReleaseStatus(ctx context.Context, releaseID string, status Status, publishedAt *time.Time, now time.Time) error
	// TouchRelease moves updated_at.
	TouchRelease(ctx context.Context, releaseID string, now time.Time) error
	// DeleteRelease deletes the Release row and what the database cascades.
	DeleteRelease(ctx context.Context, releaseID string) error
	// ContentDocumentID reads the Release's content document reference with
	// lock.
	ContentDocumentID(ctx context.Context, releaseID string, lock RowLock) (*uuid.UUID, error)
	// SourceLocale reads the Release's source locale with lock.
	SourceLocale(ctx context.Context, releaseID string, lock RowLock) (string, error)
	// DocumentRoot reads the Release root of an AI document with lock.
	DocumentRoot(ctx context.Context, releaseID string, lock RowLock) (DocumentRoot, error)
	// CheckResourceRoute requires that no route uses the Release slug, and
	// LockResourceRoute locks the route first.
	CheckResourceRoute(ctx context.Context, slug string) error
	LockResourceRoute(ctx context.Context, slug string) error
	// ResourceRouteAvailable reports whether no route uses the Release slug.
	ResourceRouteAvailable(ctx context.Context, slug string) (bool, error)
	// DeleteShareLinks removes the Release's share links.
	DeleteShareLinks(ctx context.Context, releaseID string) error
	// CountPerRelease counts each Release's Tracks or credits.
	CountTracksPerRelease(ctx context.Context, releaseIDs []string) (map[string]int32, error)
	CountCreditsPerRelease(ctx context.Context, releaseIDs []string) (map[string]int32, error)
	// PublishContentUpdate schedules the content.updated signal of update.
	PublishContentUpdate(ctx context.Context, update ContentUpdate) error
	// RequestOG requests the Release's current OG work.
	RequestOG(ctx context.Context, releaseID, reason string) error
}

// ReleaseLocales reads and writes the release_translation and
// release_credit_locale tables.
type ReleaseLocales interface {
	// LocaleMetadata reads one locale, locked for update when forUpdate is
	// set, and reports whether it exists.
	LocaleMetadata(ctx context.Context, releaseID, locale string, forUpdate bool) (LocaleMetadata, bool, error)
	// LocaleTitle reads one locale's title; a missing locale is
	// ErrLocaleNotFound.
	LocaleTitle(ctx context.Context, releaseID, locale string) (*string, error)
	// SaveSourceLocaleTitle stores the source locale's title, creating the
	// locale.
	SaveSourceLocaleTitle(ctx context.Context, releaseID, locale, title string, now time.Time) error
	// InsertLocale creates a locale.
	InsertLocale(ctx context.Context, releaseID, locale string, title *string, now time.Time) error
	// UpdateLocaleTitle stores the locale's title and updated_at and
	// reports whether the locale exists.
	UpdateLocaleTitle(ctx context.Context, releaseID, locale string, title *string, updatedAt time.Time) (bool, error)
	// DeleteLocale deletes the locale and its credit notes and reports
	// whether the locale existed.
	DeleteLocale(ctx context.Context, releaseID, locale string) (bool, error)
	// CreditNotes maps each credit of the Release with a note in locale to
	// it.
	CreditNotes(ctx context.Context, releaseID, locale string) (map[string]string, error)
	// ReplaceCreditNotes replaces every credit note of locale.
	ReplaceCreditNotes(ctx context.Context, releaseID, locale string, notes map[string]string, now time.Time) error
	// TranslationDefaultLocale reads the translation runtime's default
	// locale, storing the default settings when there are none.
	TranslationDefaultLocale(ctx context.Context) (string, error)
}

// ReleaseRelations reads and writes the Release relation tables.
type ReleaseRelations interface {
	// ReleaseArtists lists the Release's Artists by sort order, then Artist
	// ID; ReleaseArtistsByCreation by sort order, then when they were added.
	ReleaseArtists(ctx context.Context, releaseID string) ([]ReleaseArtist, error)
	ReleaseArtistsByCreation(ctx context.Context, releaseID string) ([]ReleaseArtist, error)
	// ReplaceReleaseArtists stores next in place of current.
	ReplaceReleaseArtists(ctx context.Context, releaseID string, current, next []ReleaseArtist) error
	// ReleaseLabels lists the Release's Labels by sort order, then Label ID.
	ReleaseLabels(ctx context.Context, releaseID string) ([]ReleaseLabel, error)
	ReplaceReleaseLabels(ctx context.Context, releaseID string, current, next []ReleaseLabel) error
	// ReleaseFormats lists the Release's Formats by Format ID.
	ReleaseFormats(ctx context.Context, releaseID string) ([]ReleaseFormat, error)
	ReplaceReleaseFormats(ctx context.Context, releaseID string, current, next []ReleaseFormat) error
	// ReleaseCredits lists the Release's credits by sort order, then ID.
	ReleaseCredits(ctx context.Context, releaseID string) ([]ReleaseCredit, error)
	ReplaceReleaseCredits(ctx context.Context, releaseID string, current, next []ReleaseCredit) error
	// CreditIDs lists the IDs of the Release's credits in ID order.
	CreditIDs(ctx context.Context, releaseID string) ([]string, error)
	// ReferenceIDs lists the IDs the relation links the Release to.
	ReferenceIDs(ctx context.Context, relation ReferenceRelation, releaseID string) ([]string, error)
	// ReplaceReferences removes and adds links of the relation.
	ReplaceReferences(ctx context.Context, relation ReferenceRelation, releaseID string, removed, added []string) error
	// References lists the relation's references by name, then ID, and
	// Formats the Formats with their descriptions.
	References(ctx context.Context, relation ReferenceRelation, releaseID string) ([]Reference, error)
	Formats(ctx context.Context, releaseID string) ([]FormatReference, error)
	// EditorCredits lists the Release's credits with the credited Members'
	// names by sort order, creation and ID.
	EditorCredits(ctx context.Context, releaseID string) ([]EditorCredit, error)
	// ArtistSummaries and LabelSummaries read the summaries of the Artists
	// and Labels that exist among ids.
	ArtistSummaries(ctx context.Context, artistIDs []string) (map[string]ArtistSummary, error)
	LabelSummaries(ctx context.Context, labelIDs []string) (map[string]LabelSummary, error)
	// LockMemberReferences locks each credited Member and its Identity, in
	// Member order, and answers a reference that is no live Member.
	LockMemberReferences(ctx context.Context, references []MemberReference) error
}

// TrackRows reads and writes the track and track_credit tables. A missing
// Track is ErrTrackNotFound.
type TrackRows interface {
	// Tracks lists the Release's Tracks by track number.
	Tracks(ctx context.Context, releaseID string) ([]Track, error)
	// FindTrack reads the Track row.
	FindTrack(ctx context.Context, trackID string) (Track, error)
	// LockTrack locks the Track row for update and reads it.
	LockTrack(ctx context.Context, trackID string) (Track, error)
	// LockTrackID locks the Track row for update and reads only its ID.
	LockTrackID(ctx context.Context, trackID string) error
	// LockTrackIDs locks the Release's Tracks for update and lists their
	// IDs.
	LockTrackIDs(ctx context.Context, releaseID string) ([]string, error)
	// LockTrackDownloadCriteriaSet locks the Member criteria of the Track's
	// download policy for update, in ID order.
	LockTrackDownloadCriteriaSet(ctx context.Context, trackID string) error
	// NextTrackNumber is one past the Release's highest track number.
	NextTrackNumber(ctx context.Context, releaseID string) (int, error)
	// InsertTrack stores a new Track and fills its generated columns.
	InsertTrack(ctx context.Context, track *Track) error
	// SaveTrack stores every column of the Track.
	SaveTrack(ctx context.Context, track Track) error
	// DisableTrackDownload disables the Track's download access and
	// forgets its Member criteria.
	DisableTrackDownload(ctx context.Context, trackID string) error
	// AttachTrackAudio stores the original audio and its processing state,
	// disabling the Track's download.
	AttachTrackAudio(ctx context.Context, trackID string, audio TrackAudio) error
	// DeleteTrack deletes the Track row.
	DeleteTrack(ctx context.Context, trackID string) error
	// CountTracksOf counts the Tracks among trackIDs of the Release, and
	// CountReleaseTracks every Track of it.
	CountTracksOf(ctx context.Context, releaseID string, trackIDs []string) (int64, error)
	CountReleaseTracks(ctx context.Context, releaseID string) (int64, error)
	// TrackOrder lists the Release's Track IDs by track number.
	TrackOrder(ctx context.Context, releaseID string) ([]string, error)
	// RenumberTracks numbers trackIDs from one in their order.
	RenumberTracks(ctx context.Context, releaseID string, trackIDs []string) error
	// HasTrackUploadSessions reports whether any of the Tracks has an audio
	// upload session.
	HasTrackUploadSessions(ctx context.Context, trackIDs []string) (bool, error)
	// RequireNoTrackUploadSessions answers ErrTrackUploadSessionsChanged
	// when the Track has an audio upload session.
	RequireNoTrackUploadSessions(ctx context.Context, trackID string) error
	// TrackCredits lists the Track's credits by sort order, then ID.
	TrackCredits(ctx context.Context, trackID string) ([]TrackCredit, error)
	// TrackCreditRows lists the Track's credits with their attribution kind
	// by sort order, then creation.
	TrackCreditRows(ctx context.Context, trackID string) ([]CreditRow, error)
	// ReplaceTrackCredits stores next in place of current, removed credits
	// first, and reports each change; added credits get their IDs.
	ReplaceTrackCredits(ctx context.Context, current, next []TrackCredit) ([]TrackCreditChange, error)
}

// TargetMetadataEffect writes the Release locale rows of a target-locale
// content batch, in its transaction.
type TargetMetadataEffect func(ctx context.Context, store Store, contentChanged bool) (contentblock.MetadataEffect, error)

// MetadataEffect writes the Release metadata of a content write, in its
// transaction.
type MetadataEffect func(ctx context.Context, store Store) (contentblock.MetadataEffect, error)

// ContentFence runs before the content store locks or changes the
// Release's document rows (contentblock.DomainFence): it locks the Release
// root in the same transaction and names the source locale.
type ContentFence func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error)

// ContentDocuments is the Release's content document in the shared content
// store (contentblock.Store), bound to the Release transaction. Each fence
// runs before the content rows are locked.
type ContentDocuments interface {
	CreateContentDocument(ctx context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error)
	// LockedContentSnapshot share-locks and reads the document in the
	// transaction, after the caller locked the Release root.
	LockedContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	// ContentSnapshot reads the document in one snapshot of its own.
	ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence ContentFence) (contentblock.Result, error)
	ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence ContentFence, effect MetadataEffect) (contentblock.Result, error)
	ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence ContentFence, effect TargetMetadataEffect) (contentblock.Result, error)
	ReplaceContentSnapshot(ctx context.Context, replace contentblock.ReplaceInput, fence ContentFence) (contentblock.Result, error)
	AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence ContentFence, effect MetadataEffect) (contentblock.AdvanceResult, error)
	DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence ContentFence) error
}

// ReleaseMedia is the Release artwork in release_file and the shared File,
// public asset and OG lifecycles of it.
type ReleaseMedia interface {
	// FileID reads the File's ID as the file row stores it; a missing File
	// is ErrFileNotFound.
	FileID(ctx context.Context, fileID string) (string, error)
	// ArtworkFile reads the Release's artwork File, the first by File ID;
	// false when it has none.
	ArtworkFile(ctx context.Context, releaseID string) (string, bool, error)
	// SetArtworkFile replaces the Release's artwork Files with fileID, and
	// ClearArtworkFiles removes them.
	SetArtworkFile(ctx context.Context, releaseID, fileID string) error
	ClearArtworkFiles(ctx context.Context, releaseID string) error
	LockAttachableFiles(ctx context.Context, fileIDs []string) error
	// BindArtwork binds the ready artwork asset of fileID to the Release and
	// returns it.
	BindArtwork(ctx context.Context, fileID, releaseID string) (*mediaasset.Reference, error)
	// ReleaseArtworkBinding releases the Release's artwork asset binding.
	ReleaseArtworkBinding(ctx context.Context, releaseID string) error
	// CancelOG cancels the Release's OG work and releases its image.
	CancelOG(ctx context.Context, releaseID string) error
	// ReadyArtwork resolves the ready artwork asset of the Release's first
	// artwork File by sort order; nil when it has none. ReadyArtworks
	// resolves the newest ready artwork asset of each Release.
	ReadyArtwork(ctx context.Context, releaseID string) (*mediaasset.Reference, error)
	ReadyArtworks(ctx context.Context, releaseIDs []string) (map[string]*mediaasset.Reference, error)
}

// ReleaseCollaboration is the shared collaboration and Member authority a
// Release save checks in its transaction.
type ReleaseCollaboration interface {
	// ResolveSessionPrincipal resolves a collaboration session's principal;
	// an invalid session is auth.ErrSessionPrincipalInvalid.
	ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error)
	// RequireCurrentContributors requires that the contributors may still
	// save the Release's room.
	RequireCurrentContributors(ctx context.Context, releaseID string, contributors []string) error
	// LockMembers key-share-locks the existing Members among memberIDs and
	// lists them.
	LockMembers(ctx context.Context, memberIDs []string) ([]string, error)
	// MemberPreferredLocale reads the live Member's preferred locale.
	MemberPreferredLocale(ctx context.Context, memberID string) (*string, error)
}

// Joined is a transaction a pipeline outside Release opened, which a
// Release step joins instead of opening its own: a translation delivery or
// interchange, a source-locale switch, or a File ingest. Store and Audit
// are both bound to that transaction, so the step passes a nil transaction
// to domainaudit. releasepostgres.Bind binds a gorm transaction; a test
// joins the fake Store and an audittest.Recorder.
type Joined struct {
	Store Store
	Audit domainaudit.Appender
}
