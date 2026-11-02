package work

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/identitystate"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/structured"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
)

// Store is what the Work use cases read and write, bound to one transaction
// or read snapshot. workpostgres implements it over the work tables and the
// shared content, media, OG, route, share link and collaboration runtimes a
// Work transaction drives; worktest is the in-memory fake.
type Store interface {
	WorkRows
	WorkLocales
	WorkCredits
	WorkClients
	WorkVersions
	ContentDocuments
	WorkMedia
	WorkCollaboration

	// PublishContentUpdate schedules the content.updated signal of update.
	PublishContentUpdate(ctx context.Context, update ContentUpdate) error
}

// AdminListPage is one page of the admin Work list: filtered (has_slug and
// has_featured_image test presence), sorted, ties broken by ID, and paged.
type AdminListPage struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Limit   int32
	Offset  int32
}

// WorkChange is what an update writes to the Work row; a nil field is left
// alone. A set Clear field writes NULL.
type WorkChange struct {
	Slug       *string
	ClearSlug  bool
	Type       *string
	Metadata   structured.Fields
	Featured   *bool
	Year       *int32
	Month      *int32
	UntilYear  OptionalInt32
	UntilMonth OptionalInt32
	IsPresent  *bool
	MapPlaceID *string
	ClearPlace bool
	UpdatedAt  time.Time
}

// OptionalInt32 is a nullable column an update may set: Set writes Value,
// which nil clears.
type OptionalInt32 struct {
	Set   bool
	Value *int32
}

// WorkRows reads and writes the work table. A missing row is
// ErrWorkNotFound.
type WorkRows interface {
	// FindWork reads the Work row.
	FindWork(ctx context.Context, workID string) (Work, error)
	// LockWork locks the Work row for update and reads it.
	LockWork(ctx context.Context, workID string, lock RowLock) (Work, error)
	// WorkExists reports whether the Work row exists.
	WorkExists(ctx context.Context, workID string) (bool, error)
	// LockRoot locks the Work row for update and reads its status.
	LockRoot(ctx context.Context, workID string) (Root, error)
	// ContentRoot reads the Work's content document root with lock.
	ContentRoot(ctx context.Context, workID string, lock RowLock) (Root, error)
	// LockDocumentRoot locks the Work whose content document is documentID.
	LockDocumentRoot(ctx context.Context, documentID uuid.UUID) (Root, error)
	// SourceLocale reads the Work's source locale; LockSourceLocale locks
	// the row for update first.
	SourceLocale(ctx context.Context, workID string) (string, error)
	LockSourceLocale(ctx context.Context, workID string) (string, error)
	// ListWorks lists one page of the admin list and counts every match.
	ListWorks(ctx context.Context, page AdminListPage) ([]Work, int64, error)
	// CountCredits and CountClients count each Work's credits and clients.
	CountCredits(ctx context.Context, workIDs []string) (map[string]int32, error)
	CountClients(ctx context.Context, workIDs []string) (map[string]int32, error)
	// SlugTaken reports whether a Work other than excludeID holds slug.
	SlugTaken(ctx context.Context, slug string, excludeID *string) (bool, error)
	// InsertWork stores a new Work and fills its ID and timestamps; a held
	// slug is ErrSlugTaken.
	InsertWork(ctx context.Context, work *Work) error
	// UpdateWork writes change to the row; a held slug is ErrSlugTaken.
	UpdateWork(ctx context.Context, workID string, change WorkChange) error
	// SetStatus stores the lifecycle status, published_at when publishedAt
	// is set, and updated_at.
	SetStatus(ctx context.Context, workID string, status Status, publishedAt *time.Time, now time.Time) error
	// SetFeaturedImage stores the featured image File, nil clearing it, and
	// updated_at; a row that is gone is ErrWorkNotFound.
	SetFeaturedImage(ctx context.Context, workID string, fileID *string, now time.Time) error
	// FeaturedImageFileID reads the Work's featured image File.
	FeaturedImageFileID(ctx context.Context, workID string) (*string, error)
	// TouchWork moves updated_at; StampWork moves it through the content
	// mutation's statement.
	TouchWork(ctx context.Context, workID string, now time.Time) error
	StampWork(ctx context.Context, workID string, now time.Time) error
	// SwitchSourceLocale moves the source locale from one locale to
	// another; a source that moved meanwhile is ErrRowChanged.
	SwitchSourceLocale(ctx context.Context, workID, from, to string, now time.Time) error
	// DeleteWork deletes the Work row.
	DeleteWork(ctx context.Context, workID string) error
	// MapPlaceExists reports whether the Map Place exists.
	MapPlaceExists(ctx context.Context, mapPlaceID string) (bool, error)
	// CheckResourceRoute requires that no route uses the Work slug, and
	// LockResourceRoute locks the route first.
	CheckResourceRoute(ctx context.Context, slug string) error
	LockResourceRoute(ctx context.Context, slug string) error
	// ResourceRouteAvailable reports whether no route uses the Work slug.
	ResourceRouteAvailable(ctx context.Context, slug string) (bool, error)
	// PreferredLocale reads the Member's preferred locale.
	PreferredLocale(ctx context.Context, memberID string) (*string, error)
	// DefaultLocale reads the translation runtime's default locale.
	DefaultLocale(ctx context.Context) (string, error)
	// DeleteShareLinks removes the Work's share links.
	DeleteShareLinks(ctx context.Context, workID string) error
}

// WorkLocales reads and writes the work_translation table.
type WorkLocales interface {
	// SourceMetadata maps each Work with a source-locale row to it.
	SourceMetadata(ctx context.Context, workIDs []string) (map[string]LocaleMetadata, error)
	// SourceDocuments maps each Work with a source-locale row to its title,
	// summary and OG image.
	SourceDocuments(ctx context.Context, workIDs []string) (map[string]SourceDocument, error)
	// LocaleMetadata reads one locale with its updated_at, locked for
	// update when forUpdate is set, and reports whether it exists.
	LocaleMetadata(ctx context.Context, workID, locale string, forUpdate bool) (LocaleMetadata, bool, error)
	// LockLocaleText locks one locale for update and reads its title and
	// summary; a missing locale is ErrLocaleNotFound.
	LockLocaleText(ctx context.Context, workID, locale string) (LocaleMetadata, error)
	// LockedLocaleText is LockLocaleText through the statement the AI
	// document and interchange paths take, reporting whether it exists.
	LockedLocaleText(ctx context.Context, workID, locale string) (LocaleMetadata, bool, error)
	// LocaleUpdatedAt reads one locale's updated_at; a missing locale is
	// ErrLocaleNotFound.
	LocaleUpdatedAt(ctx context.Context, workID, locale string) (time.Time, error)
	// InsertSourceLocale stores the first source-locale row of a Work.
	InsertSourceLocale(ctx context.Context, workID, locale, title string, summary *string, now time.Time) error
	// UpsertSourceLocale stores the source locale's title and summary.
	UpsertSourceLocale(ctx context.Context, workID, locale string, title, summary *string, now time.Time) error
	// UpdateLocaleText writes the set title and summary and updated_at; a
	// missing row is ErrRowChanged.
	UpdateLocaleText(ctx context.Context, workID, locale string, change LocaleTextChange) error
	// RestoreLocaleText writes title, summary and updated_at of a version
	// restore; a missing row is ErrRowChanged.
	RestoreLocaleText(ctx context.Context, workID, locale string, title, summary *string, now time.Time) error
	// InsertTargetLocale creates a target locale; a statement that stored
	// nothing is ErrRowChanged.
	InsertTargetLocale(ctx context.Context, workID string, metadata LocaleMetadata) error
	// UpdateTargetLocale writes the set fields and updated_at of a target
	// locale; a missing row is ErrRowChanged.
	UpdateTargetLocale(ctx context.Context, workID, locale string, change LocaleTextChange) error
	// DeleteTargetLocale deletes a target locale; a missing row is
	// ErrRowChanged.
	DeleteTargetLocale(ctx context.Context, workID, locale string) error
}

// LocaleTextChange is what a locale write stores; a field is written when
// its Set flag is.
type LocaleTextChange struct {
	SetTitle   bool
	Title      *string
	SetSummary bool
	Summary    *string
	UpdatedAt  time.Time
}

// WorkCredits reads and writes the credit groups and credits of a Work.
type WorkCredits interface {
	// CreditRows lists the Work's groups and credits by sort order and ID.
	CreditRows(ctx context.Context, workID string) ([]CreditGroup, []Credit, error)
	// FindCreditGroup reads a group; LockCreditGroup locks the group of the
	// Work for update. A missing group is ErrCreditGroupNotFound.
	FindCreditGroup(ctx context.Context, groupID string) (CreditGroup, error)
	LockCreditGroup(ctx context.Context, groupID, workID string) (CreditGroup, error)
	// CreditGroupInWork reports whether the group belongs to the Work.
	CreditGroupInWork(ctx context.Context, workID, groupID string) (bool, error)
	// CreditItemExists reports whether a group or credit of any Work has
	// the ID.
	CreditItemExists(ctx context.Context, itemID string) (bool, error)
	InsertCreditGroup(ctx context.Context, group *CreditGroup) error
	RenameCreditGroup(ctx context.Context, groupID, name string) error
	DeleteCreditGroup(ctx context.Context, groupID string) error
	// FindCredit reads a credit; LockCredit locks the credit of the Work for
	// update. A missing credit is ErrCreditNotFound.
	FindCredit(ctx context.Context, creditID string) (Credit, error)
	LockCredit(ctx context.Context, creditID, workID string) (Credit, error)
	InsertCredit(ctx context.Context, credit *Credit) error
	// UpdateCredit writes the credit's group when setGroup is set and its
	// role when role is set.
	UpdateCredit(ctx context.Context, creditID string, setGroup bool, groupID *string, role *string) error
	// UngroupCredit moves the credit out of every group.
	UngroupCredit(ctx context.Context, creditID, workID string) error
	DeleteCredit(ctx context.Context, creditID string) error
	// SetGroupSortOrder and SetCreditSortOrder store one item's place in
	// the order; a row of another Work or none is ErrRowChanged.
	SetGroupSortOrder(ctx context.Context, workID, groupID string, sortOrder int) error
	SetCreditSortOrder(ctx context.Context, workID, creditID string, groupID *string, sortOrder int) error
	// LockCreditMember key-share-locks a credited Member, answering one that
	// cannot be credited (authorizationtarget.LockReferences).
	LockCreditMember(ctx context.Context, memberID string) error
	// CreditArtist reads a credited Artist; ok is false when it is gone.
	CreditArtist(ctx context.Context, artistID string) (CreditArtistRow, bool, error)
	// CreditingArtistIDs lists the Artists the Member owns or manages.
	CreditingArtistIDs(ctx context.Context, memberID string) ([]string, error)
	// ListCreditedWorks lists one page of the credits of the Member or of
	// artistIDs and counts every match.
	ListCreditedWorks(ctx context.Context, page CreditedPage) ([]CreditedRow, int64, error)
}

// CreditedPage is one page of the credits ListMyCreditedWorks lists.
type CreditedPage struct {
	MemberID  string
	ArtistIDs []string
	Filters   []*commonv1.FilterSpec
	Sorts     []*commonv1.SortSpec
	Limit     int32
	Offset    int32
}

// CreditedRow is one credit with its Work.
type CreditedRow struct {
	WorkID            string
	SourceLocale      string
	Title             string
	Slug              *string
	Type              string
	Status            string
	CreatedAt         time.Time
	CreditID          string
	CreditRole        *string
	ArtistID          *string
	ArtistName        *string
	ArtistImageFileID *string
	MemberID          *string
}

// WorkClients reads and writes the work_client table.
type WorkClients interface {
	// ClientIDs lists the Work's clients in order.
	ClientIDs(ctx context.Context, workID string) ([]string, error)
	// Clients lists the Work's clients in order, with their logos.
	Clients(ctx context.Context, workID string) ([]ClientRow, error)
	// CountExistingClients counts the clients of clientIDs that exist.
	CountExistingClients(ctx context.Context, clientIDs []string) (int64, error)
	// ReplaceClients replaces the Work's clients, in order.
	ReplaceClients(ctx context.Context, workID string, clientIDs []string) error
}

// WorkVersions reads and writes the work_version table.
type WorkVersions interface {
	CountVersions(ctx context.Context, workID string) (int64, error)
	// ListVersions lists a page of versions, newest first.
	ListVersions(ctx context.Context, workID string, limit, offset int32) ([]Version, error)
	// FindVersion reads a version; a missing one is ErrVersionNotFound.
	FindVersion(ctx context.Context, versionID string) (Version, error)
	// LockLatestVersion locks the newest version for update; ok is false
	// when there is none.
	LockLatestVersion(ctx context.Context, workID string) (Version, bool, error)
	InsertVersion(ctx context.Context, version *Version) error
	// ContributorNicknames maps each existing Member of memberIDs to its
	// nickname.
	ContributorNicknames(ctx context.Context, memberIDs []string) (map[string]string, error)
}

// ContentFence runs before the content store locks or changes the Work's
// document rows (contentblock.DomainFence).
type ContentFence func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error)

// MetadataEffect writes the Work metadata of a content mutation, in its
// transaction.
type MetadataEffect func(ctx context.Context, store Store) (contentblock.MetadataEffect, error)

// TargetMetadataEffect writes the Work locale row of a target-locale
// content batch, in its transaction.
type TargetMetadataEffect func(ctx context.Context, store Store, contentChanged bool) (contentblock.MetadataEffect, error)

// ContentDocuments is the Work's content document in the shared content
// store (contentblock.Store), bound to the Work transaction.
type ContentDocuments interface {
	CreateContentDocument(ctx context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error)
	// LockedContentSnapshot share-locks and reads the document in the
	// transaction; ContentSnapshot reads it in one snapshot of its own.
	LockedContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	// MediaReferences lists the File references of the document's Blocks.
	MediaReferences(ctx context.Context, documentID uuid.UUID) ([]*fileref.BlockMediaItem, error)
	// LocaleOwnershipConflict is the conflict of a room whose source locale
	// moved (contentblock.LocaleOwnershipConflict).
	LocaleOwnershipConflict(ctx context.Context, documentID uuid.UUID, message string) error
	ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence ContentFence) (contentblock.Result, error)
	ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence ContentFence, effect MetadataEffect) (contentblock.Result, error)
	ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence ContentFence, effect TargetMetadataEffect) (contentblock.Result, error)
	ReplaceContentSnapshot(ctx context.Context, replace contentblock.ReplaceInput, fence ContentFence) (contentblock.Result, error)
	AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence ContentFence, effect MetadataEffect) (contentblock.AdvanceResult, error)
	DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence ContentFence) error
}

// WorkMedia is the shared File, public asset and OG lifecycle of a Work.
type WorkMedia interface {
	// ReadyAssets resolves ready public assets by ID.
	ReadyAssets(ctx context.Context, assetIDs []string) (map[string]*mediaasset.Reference, error)
	// ReadyFileAsset resolves the ready public asset of a source File of
	// kind.
	ReadyFileAsset(ctx context.Context, fileID, kind string) (*mediaasset.Reference, error)
	// LockAttachableFile locks a File a Work may attach; FileID reads its
	// stored ID, a missing File answering ErrFileNotFound.
	LockAttachableFile(ctx context.Context, fileID string) error
	FileID(ctx context.Context, fileID string) (string, error)
	// BindFeaturedImage binds the File's ready image as the featured image;
	// ReleaseFeaturedImage releases the binding.
	BindFeaturedImage(ctx context.Context, workID, fileID string) (*mediaasset.Reference, error)
	ReleaseFeaturedImage(ctx context.Context, workID string) error
	// UnavailableAttachmentKinds names the attachments of a version
	// document that are no longer available.
	UnavailableAttachmentKinds(ctx context.Context, document *contentv1.LocalizedRichTextDocument) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error)
	// HydrateBlockMedia adds the editor delivery of block media the
	// principal may read.
	HydrateBlockMedia(ctx context.Context, workID string, documentID uuid.UUID, principal *auth.UserInfo, items []*fileref.BlockMediaItem) ([]*fileref.BlockMediaItem, error)
	// RequestOG starts the Work's current OG generation for locale and
	// returns its run ID, empty when none started; CancelOG cancels it and
	// releases its image.
	RequestOG(ctx context.Context, workID, locale, reason string) (string, error)
	CancelOG(ctx context.Context, workID string) error
}

// WorkCollaboration is the shared session, fence and collaboration
// authority a Work transaction checks.
type WorkCollaboration interface {
	// RequireFreshCan checks can for the request principal with its
	// Identity locked through fence in the transaction
	// (identitystate.RequireFreshCan), for a check outside a mutation's
	// Access step.
	RequireFreshCan(ctx context.Context, fence identitystate.Fence, checker authz.AuthorizationDecisionChecker, can policyv1.Can) error
	// ResolveSessionPrincipal resolves a collaboration session's principal;
	// an invalid session is auth.ErrSessionPrincipalInvalid.
	ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error)
	// LockSession locks the session for its principal; one that no longer
	// belongs to it is auth.ErrSessionPrincipalInvalid.
	LockSession(ctx context.Context, sessionID string, principal *auth.UserInfo) error
	// RequireCurrentContributors requires that the contributors may still
	// save the Work's room.
	RequireCurrentContributors(ctx context.Context, workID string, contributors []string) error
	// LockContributors key-share-locks the contributor Members of a save
	// and returns the IDs that exist.
	LockContributors(ctx context.Context, memberIDs []string) ([]string, error)
}

// Joined is a transaction a pipeline outside Work opened, which a Work step
// joins instead of opening its own: a translation apply or interchange, or
// another domain's policy check. Store and Audit are both bound to that
// transaction, so the step passes a nil transaction to domainaudit.
// workpostgres.Bind binds a gorm transaction; a test joins the fake Store
// and an audittest.Recorder.
type Joined struct {
	Store Store
	Audit domainaudit.Appender
}
