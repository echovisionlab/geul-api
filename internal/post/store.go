package post

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/versionrestore"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// Store is what the Post use cases read and write, bound to one transaction
// or read snapshot. postpostgres implements it over the post tables and the
// shared content, media, OG, route and translation runtimes a Post
// transaction drives; posttest is the in-memory fake.
type Store interface {
	PostRows
	PostLocales
	PostRelations
	CommentReads
	CommentWrites
	PostVersions
	ContentDocuments
	PostMedia
	PostCollaboration

	// PublishContentUpdate schedules the content.updated signal of update.
	PublishContentUpdate(ctx context.Context, update ContentUpdate) error
	// AppendMemberAudit appends a record attributed to memberID through
	// writer, for a delivery that runs in another domain's transaction.
	AppendMemberAudit(ctx context.Context, writer domainaudit.Appender, memberID string, action sharedtelemetry.AuditAction, build domainaudit.Builder) error
}

// The answers the Store gives for a missing row or a lost condition.
var (
	ErrPostNotFound    = errors.New("post not found")
	ErrFileNotFound    = errors.New("post featured image file not found")
	ErrCommentNotFound = errors.New("comment not found")
	ErrVersionNotFound = errors.New("post version not found")
	ErrLocaleNotFound  = errors.New("post locale not found")
	// ErrSlugTaken is a new Post whose slug another Post holds.
	ErrSlugTaken = errors.New("post slug taken")
)

// RowLock is the lock a read takes on the rows it reads.
type RowLock string

const (
	Unlocked      RowLock = ""
	KeyShareLock  RowLock = "KEY SHARE"
	UpdateLock    RowLock = "UPDATE"
	ShareRowsLock RowLock = "SHARE"
)

// DocumentRoot is the Post root a content mutation locks.
type DocumentRoot struct {
	ID                string
	Status            Status
	ContentDocumentID *uuid.UUID
	SourceLocale      string
}

// AIDocumentRoot is the Post root an AI document read or mutation locks.
type AIDocumentRoot struct {
	ID                string
	Status            Status
	ContentDocumentID *string
}

// CommentPost is what a comment read or write checks on its Post.
type CommentPost struct {
	ID              string
	Status          Status
	CommentsEnabled bool
}

// SourceLocaleState is the source locale row of a Post.
type SourceLocaleState struct {
	Title     *string
	Summary   *string
	OgAssetID *string
}

// LocaleMetadata is one post_translation row's title and summary.
type LocaleMetadata struct {
	Locale    string
	Title     *string
	Summary   *string
	UpdatedAt time.Time
}

// SettingsChange is the set of Post settings an update writes. A nil field
// is left alone; Slug and MapPlaceID set to a nil value clear the column.
type SettingsChange struct {
	Slug            *OptionalString
	CommentsEnabled *bool
	MapPlaceID      *OptionalString
	DocumentLayout  *DocumentLayout
	UpdatedAt       time.Time
}

// OptionalString is a nullable column value: nil Value stores NULL.
type OptionalString struct{ Value *string }

// LifecycleChange is a status move a Post lifecycle mutation writes.
type LifecycleChange struct {
	Status    Status
	UpdatedAt time.Time
	// ClearSchedule clears scheduled_at and scheduled_time_zone.
	ClearSchedule bool
	// ScheduledAt and ScheduledTimeZone, when set, store a schedule.
	ScheduledAt       *time.Time
	ScheduledTimeZone *string
	// PublishedAt, when set, stores the first publication time.
	PublishedAt *time.Time
}

// Taxonomy names the Category or Tag relation of a Post.
type Taxonomy string

const (
	Categories Taxonomy = "category"
	Tags       Taxonomy = "tag"
)

// AuthorLink is one durable author of a listed Post.
type AuthorLink struct {
	PostID   string
	MemberID string
}

// ParticipantRow is one durable author or collaborator relation with
// whether its Member still resolves to an active, unbanned Identity.
type ParticipantRow struct {
	MemberID              string
	IdentityID            string
	Role                  ParticipantRole
	CreatedAt             time.Time
	HasEffectiveAuthority bool
}

// ListQuery selects one page of Posts. Scope, when set, limits the list
// to the Posts a principal may view.
type ListQuery struct {
	Filters  []*commonv1.FilterSpec
	Sorts    []*commonv1.SortSpec
	Limit    int32
	Offset   int32
	ByAuthor bool
	Scope    *ViewScope
	// Series also loads each Post's Series.
	Series bool
}

// ViewScope is the Posts a principal may view: ViewIDs while not archived,
// ArchivedIDs while archived.
type ViewScope struct {
	ViewIDs     []string
	ArchivedIDs []string
}

// AIDocumentQuery selects one page of the Posts a principal may view,
// titled like Search when it is set.
type AIDocumentQuery struct {
	Scope  ViewScope
	Search string
	Limit  int
	Offset int
}

// StoredVersion is one post_version row.
type StoredVersion struct {
	ID                   string
	PostID               string
	Version              int32
	ContentSnapshot      json.RawMessage
	ContributorMemberIDs []string
	CreatedAt            time.Time
}

// ContentUpdate is a content.updated signal of a Post locale change.
type ContentUpdate struct {
	PostID               string
	Fields               []string
	DocumentRevision     string
	Contributors         []string
	Source               UpdateSource
	Locale               string
	LocaleDeleted        bool
	TargetRevision       *string
	DocumentStateChanged bool
}

// UpdateSource is what made a content change.
type UpdateSource int

const (
	SourceManage UpdateSource = iota + 1
	SourceCollaboration
	SourceAI
)

// PostRows reads and writes the post table.
type PostRows interface {
	// FindPost reads the Post with its Categories, Tags and Series. A
	// missing Post is ErrPostNotFound, as for every read of one Post.
	FindPost(ctx context.Context, postID string) (Post, error)
	// ReadPost reads the Post row, TakePost the same row without ordering.
	ReadPost(ctx context.Context, postID string) (Post, error)
	TakePost(ctx context.Context, postID string) (Post, error)
	// FindPostID reads only the Post's ID, to answer a missing Post.
	FindPostID(ctx context.Context, postID string) error
	// PostStatus reads the Post's status.
	PostStatus(ctx context.Context, postID string) (Status, error)
	// LockPostStatus locks the Post row for update and reads its status;
	// LockParticipantRoot is the same lock taken by a participant or
	// comment change.
	LockPostStatus(ctx context.Context, postID string) (Status, error)
	LockParticipantRoot(ctx context.Context, postID string) (Status, error)
	// KeySharePostStatus key-share-locks the Post row and reads its status.
	KeySharePostStatus(ctx context.Context, postID string) (Status, error)
	// KeyShareLockPost key-share-locks the Post row and reads it.
	KeyShareLockPost(ctx context.Context, postID string) (Post, error)
	// LockPostRoot locks the Post row for update before its locale rows.
	LockPostRoot(ctx context.Context, postID string) error
	// DocumentRoot reads the Post root of a content mutation, and
	// LockDocumentRoot locks it for update first.
	DocumentRoot(ctx context.Context, postID string) (DocumentRoot, error)
	LockDocumentRoot(ctx context.Context, postID string) (DocumentRoot, error)
	// AIDocumentRoot reads the Post root of an AI document with lock.
	AIDocumentRoot(ctx context.Context, postID string, lock RowLock) (AIDocumentRoot, error)
	// SourceLocale reads the Post's source locale.
	SourceLocale(ctx context.Context, postID string) (string, error)
	// SwitchSourceLocale moves the Post's source locale from one locale to
	// another and reports how many rows it changed.
	SwitchSourceLocale(ctx context.Context, postID, from, to string, now time.Time) (int64, error)
	// ListPosts lists one page of Posts and counts every match.
	ListPosts(ctx context.Context, query ListQuery) ([]Post, int64, error)
	// ListAIDocuments lists one page of the Posts query scopes and counts
	// every match.
	ListAIDocuments(ctx context.Context, query AIDocumentQuery) ([]AIDocumentListItem, int64, error)
	// InsertPost stores a new Post and fills its ID, revision and
	// timestamps. A slug another Post holds is ErrSlugTaken.
	InsertPost(ctx context.Context, post *Post) error
	// UpdateSettings stores change on post and reads the row back into it.
	UpdateSettings(ctx context.Context, post *Post, change SettingsChange) error
	// UpdateLifecycle stores a lifecycle move.
	UpdateLifecycle(ctx context.Context, postID string, change LifecycleChange) error
	// FeaturedImageFileID reads the Post's featured image File.
	FeaturedImageFileID(ctx context.Context, postID string) (*string, error)
	// SetFeaturedImage stores fileID, or clears it when nil, with
	// updated_at.
	SetFeaturedImage(ctx context.Context, postID string, fileID *string, now time.Time) error
	// TouchPost moves updated_at.
	TouchPost(ctx context.Context, postID string, now time.Time) error
	// DeletePost deletes the Post row.
	DeletePost(ctx context.Context, postID string) error
	// DatabaseNow reads the database's transaction timestamp.
	DatabaseNow(ctx context.Context) (time.Time, error)
	// LockDuePosts locks, skipping locked rows, up to limit scheduled
	// Posts whose time has come, oldest schedule first.
	LockDuePosts(ctx context.Context, limit int) ([]Post, error)
	// PublishDuePost publishes a due scheduled Post, storing publishedAt
	// when set, and reports whether it was still due.
	PublishDuePost(ctx context.Context, postID string, publishedAt *time.Time, now time.Time) (bool, error)
	// SlugTaken reports whether a Post other than excludeID holds slug.
	SlugTaken(ctx context.Context, slug, excludeID string) (bool, error)
	// RouteAvailable reports whether no Page occupies the Post route of
	// slug; CheckRoute requires it and LockRoute locks the route first.
	RouteAvailable(ctx context.Context, slug string) (bool, error)
	CheckRoute(ctx context.Context, slug string) error
	LockRoute(ctx context.Context, slug string) error
	// MapPlaceExists reports whether the Map Place exists.
	MapPlaceExists(ctx context.Context, mapPlaceID string) (bool, error)
}

// PostLocales reads and writes the post_translation table.
type PostLocales interface {
	// SourceLocaleStates reads the source locale row of each Post that has
	// one.
	SourceLocaleStates(ctx context.Context, postIDs []string) (map[string]SourceLocaleState, error)
	// UpsertSourceLocale stores the source locale row's title and summary.
	UpsertSourceLocale(ctx context.Context, postID, locale string, title, summary *string, now time.Time) error
	// LocaleMetadata reads one locale row, locked for update when
	// forUpdate is set. A missing row is ErrLocaleNotFound.
	LocaleMetadata(ctx context.Context, postID, locale string, forUpdate bool) (LocaleMetadata, error)
	// OptionalLocaleMetadata reads one locale row with lock and reports
	// whether it exists.
	OptionalLocaleMetadata(ctx context.Context, postID, locale string, forUpdate bool) (LocaleMetadata, bool, error)
	// LocaleText reads one locale's title and summary, locked for update
	// when forUpdate is set, and reports whether it exists.
	LocaleText(ctx context.Context, postID, locale string, forUpdate bool) (AIDocumentLocaleMetadata, bool, error)
	// CountLocale counts the locale's rows.
	CountLocale(ctx context.Context, postID, locale string) (int64, error)
	// UpdateLocaleMetadata stores the locale's title and summary when the
	// matching Set is true, with updatedAt, and reports how many rows it
	// changed.
	UpdateLocaleMetadata(ctx context.Context, postID, locale string, change LocaleChange) (int64, error)
	// InsertLocale creates a locale row with title and summary and
	// reports how many rows it created.
	InsertLocale(ctx context.Context, postID, locale string, title, summary *string, now time.Time) (int64, error)
	// InsertEmptyLocale creates an untitled locale row.
	InsertEmptyLocale(ctx context.Context, postID, locale string, now time.Time) error
	// UpsertLocaleText stores the locale's title and summary, creating it.
	UpsertLocaleText(ctx context.Context, postID, locale string, title, summary *string, now time.Time) error
	// DeleteLocale deletes the locale row and reports how many rows it
	// removed.
	DeleteLocale(ctx context.Context, postID, locale string) (int64, error)
}

// LocaleChange is a write of one locale row.
type LocaleChange struct {
	SetTitle   bool
	Title      *string
	SetSummary bool
	Summary    *string
	UpdatedAt  time.Time
}

// PostRelations reads and writes the Post's Categories, Tags, authors and
// collaborators.
type PostRelations interface {
	// TaxonomyIDs lists the Post's Category and Tag IDs, each ordered.
	TaxonomyIDs(ctx context.Context, postID string) (categoryIDs, tagIDs []string, err error)
	// CountTaxonomy counts the Categories or Tags among ids.
	CountTaxonomy(ctx context.Context, taxonomy Taxonomy, ids []string) (int64, error)
	// InsertTaxonomyLinks files the new Post under ids, one row each.
	InsertTaxonomyLinks(ctx context.Context, taxonomy Taxonomy, postID string, ids []string) error
	// ReplaceTaxonomyLinks replaces the Post's links with ids.
	ReplaceTaxonomyLinks(ctx context.Context, taxonomy Taxonomy, postID string, ids []string) error
	// AuthorLinks lists the authors of each Post, oldest first.
	AuthorLinks(ctx context.Context, postIDs []string) ([]AuthorLink, error)
	// CommentCounts counts each Post's comments.
	CommentCounts(ctx context.Context, postIDs []string) (map[string]int32, error)
	// ListParticipants lists the Post's authors, then collaborators, oldest
	// first.
	ListParticipants(ctx context.Context, postID string) ([]ParticipantRow, error)
	// ParticipantCreatedAt is when memberID took role on the Post, or the
	// zero time.
	ParticipantCreatedAt(ctx context.Context, postID, memberID string, role ParticipantRole) (time.Time, error)
	// ParticipantExists reports whether memberID holds role on the Post.
	ParticipantExists(ctx context.Context, postID, memberID string, role ParticipantRole) (bool, error)
	// CountAuthors counts the Post's authors, only memberID's relation
	// when it is set.
	CountAuthors(ctx context.Context, postID, memberID string) (int64, error)
	// InsertCreatorAuthor makes memberID the first author of a new Post.
	InsertCreatorAuthor(ctx context.Context, postID, memberID string) error
	// InsertParticipant gives memberID role on the Post.
	InsertParticipant(ctx context.Context, postID, memberID string, role ParticipantRole, createdAt time.Time) error
	// DeleteParticipant removes memberID's role relation and reports how
	// many rows it removed.
	DeleteParticipant(ctx context.Context, postID, memberID string, role ParticipantRole) (int64, error)
	// LockMemberIdentity locks the linked Identity of an active Member a
	// participant change targets and returns it
	// (authorizationtarget.RequireLocked); LockLinkedMemberIdentity
	// accepts an inactive one (authorizationtarget.RequireLockedLinked).
	// Their errors carry their answer.
	LockMemberIdentity(ctx context.Context, memberID string) (string, error)
	LockLinkedMemberIdentity(ctx context.Context, memberID string) (string, error)
	// MemberNicknames reads the nickname of each Member.
	MemberNicknames(ctx context.Context, memberIDs []string) (map[string]string, error)
}

// CommentReads reads the comments of a Post in a snapshot that already
// authorized it.
type CommentReads interface {
	// CountComments counts the Post's comments under parentID, or its
	// top-level comments when parentID is nil.
	CountComments(ctx context.Context, postID string, parentID *string) (int64, error)
	// CommentCursor reads the creation time of the cursor comment under
	// parentID, or a top-level one, and reports whether it exists.
	CommentCursor(ctx context.Context, postID string, parentID *string, cursorID string) (time.Time, bool, error)
	// ListComments lists up to limit comments of the Post under parentID,
	// or its top-level ones, oldest first, after the cursor when set.
	ListComments(ctx context.Context, postID string, parentID *string, after *CommentCursorAt, limit int) ([]Comment, error)
	// CommentDescendants lists the comments below parentIDs down to
	// maxDepth, oldest first.
	CommentDescendants(ctx context.Context, postID string, parentIDs []string, maxDepth int) ([]Comment, error)
	// FindComment reads one comment.
	FindComment(ctx context.Context, commentID string) (Comment, error)
	// FindPostComment reads one comment of the Post.
	FindPostComment(ctx context.Context, postID, commentID string) (Comment, error)
	// ShareLockCommentPost share-locks the Post of a comment read.
	ShareLockCommentPost(ctx context.Context, postID string) (CommentPost, error)
}

// ContentReads reads a Post's content document in a snapshot that already
// authorized the Post; the public reads take it.
type ContentReads interface {
	SourceLocale(ctx context.Context, postID string) (string, error)
	DocumentRoot(ctx context.Context, postID string) (DocumentRoot, error)
	ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
}

// CommentCursorAt is where a comment page continues.
type CommentCursorAt struct {
	ID        string
	CreatedAt time.Time
}

// CommentWrites writes the comments of a Post.
type CommentWrites interface {
	// LockCommentPost locks the Post a comment is written on.
	LockCommentPost(ctx context.Context, postID string) (CommentPost, error)
	// LockComment locks one comment for update.
	LockComment(ctx context.Context, commentID string) (Comment, error)
	// LockPostComment locks one comment of the Post for update.
	LockPostComment(ctx context.Context, postID, commentID string) (Comment, error)
	// CommentDepth is how deep the comment sits, counting itself.
	CommentDepth(ctx context.Context, postID, commentID string, maxDepth int) (int, error)
	// CommentPostID reads the Post of a comment.
	CommentPostID(ctx context.Context, commentID string) (string, error)
	// InsertComment stores a new comment and fills its ID and timestamps.
	InsertComment(ctx context.Context, comment *Comment) error
	// UpdateCommentContent stores the comment's content.
	UpdateCommentContent(ctx context.Context, comment Comment, content string) error
	// SoftDeleteComment marks the comment deleted and replaces its content.
	SoftDeleteComment(ctx context.Context, comment Comment, content string) error
}

// PostVersions reads and writes the post_version table.
type PostVersions interface {
	// ListVersions lists one page of the Post's versions, newest first,
	// and counts them.
	ListVersions(ctx context.Context, postID string, limit, offset int32) ([]StoredVersion, int64, error)
	// FindVersion reads one version.
	FindVersion(ctx context.Context, versionID string) (StoredVersion, error)
	// LockLatestVersion locks the Post's newest version and reports whether
	// one exists.
	LockLatestVersion(ctx context.Context, postID string) (StoredVersion, bool, error)
	// InsertVersion stores a new version and fills its ID and creation time.
	InsertVersion(ctx context.Context, version *StoredVersion) error
	// RestoreVersion replaces the Post's source content with the version
	// restore describes.
	RestoreVersion(ctx context.Context, restore VersionRestore) (versionrestore.Outcome, error)
	// UnavailableVersionAttachments reads which File attachments of a
	// version document can no longer be restored.
	UnavailableVersionAttachments(ctx context.Context, document *contentv1.LocalizedRichTextDocument) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error)
}

// VersionRestore is the restore of one version: versionrestore.Restore with
// its steps on the Store.
type VersionRestore struct {
	DocumentID         uuid.UUID
	Version            versionrestore.Source
	Fence              ContentFence
	LoadSource         func(ctx context.Context, store Store) (versionrestore.Source, error)
	SwitchSourceLocale func(ctx context.Context, store Store, from string, revision uuid.UUID) error
	Replacement        func(ctx context.Context, store Store, revision uuid.UUID) (contentblock.ReplaceInput, error)
	UpdateMetadata     func(ctx context.Context, store Store) error
}

// ContentFence runs before a content mutation locks the document rows, in
// its transaction, and answers the document's domain context.
type ContentFence func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error)

// MetadataEffect writes the Post metadata of a content mutation, in its
// transaction.
type MetadataEffect func(ctx context.Context, store Store) (contentblock.MetadataEffect, error)

// TargetMetadataEffect writes the Post locale row of a target-locale
// content batch, in its transaction.
type TargetMetadataEffect func(ctx context.Context, store Store, contentChanged bool) (contentblock.MetadataEffect, error)

// ContentDocuments is the Post's content document in the shared content
// store (contentblock.Store), bound to the Post transaction.
type ContentDocuments interface {
	CreateContentDocument(ctx context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error)
	// LockedContentSnapshot reads the document in the transaction, after
	// the caller locked the Post root.
	LockedContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	// ContentSnapshot reads the document in one snapshot of its own.
	ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	// LockContentDocument locks the document row for update.
	LockContentDocument(ctx context.Context, documentID uuid.UUID) error
	// ContentRevision reads the document's current revision.
	ContentRevision(ctx context.Context, documentID uuid.UUID) (uuid.UUID, error)
	ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence ContentFence) (contentblock.Result, error)
	ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence ContentFence, effect MetadataEffect) (contentblock.Result, error)
	ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence ContentFence, effect TargetMetadataEffect) (contentblock.Result, error)
	ReplaceContentSnapshot(ctx context.Context, replace contentblock.ReplaceInput, fence ContentFence) (contentblock.Result, error)
	AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence ContentFence, effect MetadataEffect) (contentblock.AdvanceResult, error)
	DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence ContentFence) error
}

// PostMedia is the shared File, public asset, share link and OG lifecycle
// of a Post.
type PostMedia interface {
	// LockAttachableFiles locks the Files a Post is about to reference.
	LockAttachableFiles(ctx context.Context, fileIDs []string) error
	// FindFile requires that the File exists; a missing one is
	// ErrFileNotFound.
	FindFile(ctx context.Context, fileID string) error
	// ReadyOgAssets resolves the ready public assets among assetIDs.
	ReadyOgAssets(ctx context.Context, assetIDs []string) (map[string]*mediaasset.Reference, error)
	// BlockMedia loads the content document's File attachments with the
	// delivery principal may read.
	BlockMedia(ctx context.Context, postID string, documentID uuid.UUID, principal *auth.UserInfo) ([]*fileref.BlockMediaItem, error)
	// RequestOG starts the Post's current OG generation in locale and
	// returns its run ID, nil when none started.
	RequestOG(ctx context.Context, postID, locale string, force bool, reason string) (*string, error)
	// CancelOG cancels the Post's OG generation and releases its image.
	CancelOG(ctx context.Context, postID string) error
	// DeleteShareLinks removes the Post's share links.
	DeleteShareLinks(ctx context.Context, postID string) error
}

// PostCollaboration is the shared session, Member and translation state a
// Post transaction reads.
type PostCollaboration interface {
	// ResolveSessionPrincipal resolves a collaboration session's principal;
	// an invalid session is auth.ErrSessionPrincipalInvalid.
	ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error)
	// LockContributorMembers key-share-locks the Members of a save and
	// counts those that exist.
	LockContributorMembers(ctx context.Context, memberIDs []string) (int, error)
	// ActiveContributorIdentities maps each live, onboarded Member among
	// memberIDs whose linked Identity is active and unbanned to it.
	ActiveContributorIdentities(ctx context.Context, memberIDs []string) (map[string]string, error)
	// MemberPreferredLocale reads the preferred locale of a live Member.
	MemberPreferredLocale(ctx context.Context, memberID string) (*string, error)
	// DefaultTranslationLocale reads the translation runtime's default
	// locale.
	DefaultTranslationLocale(ctx context.Context) (string, error)
}

// Comment is one stored comment.
type Comment struct {
	ID        string
	PostID    string
	MemberID  *string
	ParentID  *string
	Content   string
	IsDeleted bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Joined carries the Store and audit bound to another pipeline's transaction.
// Read-only and explicitly unaudited steps leave Audit nil.
type Joined struct {
	Store Store
	Audit domainaudit.Appender
}
