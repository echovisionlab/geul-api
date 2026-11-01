package label

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

// Store is what the Label use cases read and write, bound to one
// transaction or read snapshot. labelpostgres implements it over the label
// tables and the shared content, media, OG, route and relation runtimes the
// Label transaction drives; labeltest is the in-memory fake.
type Store interface {
	entityparticipant.Store
	LabelRows
	LabelLocales
	ContentDocuments
	LabelMedia
	LabelCollaboration

	// DeletionImpacts lists what references the Label: its child Labels,
	// then the Artists, Program Events and Releases linked to it, each
	// ordered by ID text.
	DeletionImpacts(ctx context.Context, labelID string) ([]DeletionImpact, error)
	// CountArtistsByLabel and CountReleasesByLabel count each Label's
	// linked Artists and Releases.
	CountArtistsByLabel(ctx context.Context, labelIDs []string) (map[string]int32, error)
	CountReleasesByLabel(ctx context.Context, labelIDs []string) (map[string]int32, error)
	// DeleteShareLinks removes the Label's share links.
	DeleteShareLinks(ctx context.Context, labelID string) error
	// PublishContentUpdate schedules the content.updated signal of update.
	PublishContentUpdate(ctx context.Context, update ContentUpdate) error
	// AppendMemberAudit appends a record attributed to memberID through
	// writer, for a delivery that runs in another domain's transaction.
	AppendMemberAudit(ctx context.Context, writer domainaudit.Appender, memberID string, action sharedtelemetry.AuditAction, build domainaudit.Builder) error
}

// LabelRows reads and writes the label table. A missing row is
// ErrLabelNotFound.
type LabelRows interface {
	// FindLabel reads the Label row.
	FindLabel(ctx context.Context, labelID string) (Label, error)
	// FindLabelID reads only the Label's ID, to answer a missing Label.
	FindLabelID(ctx context.Context, labelID string) (string, error)
	// FindLabelLogos reads the Label's ID and logo files.
	FindLabelLogos(ctx context.Context, labelID string) (Label, error)
	// LockLabel locks the Label row for update and reads it.
	LockLabel(ctx context.Context, labelID string) (Label, error)
	// LockLabelID locks the Label row for update.
	LockLabelID(ctx context.Context, labelID string) (string, error)
	// ShareLockLabel share-locks the Label row and reads it.
	ShareLockLabel(ctx context.Context, labelID string) (Label, error)
	// LockLabelRelations freezes the Label and every relation a deletion
	// preview counts until the transaction ends.
	LockLabelRelations(ctx context.Context) error
	// ListLabels lists one page of Labels and counts every match.
	ListLabels(ctx context.Context, page ListPage) ([]Label, int64, error)
	// SlugTaken reports whether a Label other than excludeID holds slug.
	SlugTaken(ctx context.Context, slug, excludeID string) (bool, error)
	// InsertLabel stores a new Label and fills its ID and timestamps. A
	// slug another Label holds is ErrSlugTaken.
	InsertLabel(ctx context.Context, label *Label) error
	// InsertCreatorOwner makes memberID the first Owner of a new Label.
	InsertCreatorOwner(ctx context.Context, labelID, memberID string) error
	// SetLabelStatus stores the lifecycle status, published_at when
	// publishedAt is set, and updated_at.
	SetLabelStatus(ctx context.Context, labelID string, status Status, publishedAt *time.Time, now time.Time) error
	// SetLabelLogo stores fileID, or clears it when nil, in the variant's
	// slot.
	SetLabelLogo(ctx context.Context, labelID string, variant LogoVariant, fileID *string, now time.Time) error
	// EffectiveLogoFileID is the light logo, else the dark one.
	EffectiveLogoFileID(ctx context.Context, labelID string) (*string, error)
	// ClearOgAsset forgets the Label's OG image.
	ClearOgAsset(ctx context.Context, labelID string) error
	// TouchLabel moves updated_at.
	TouchLabel(ctx context.Context, labelID string, now time.Time) error
	// DeleteLabel deletes the Label row.
	DeleteLabel(ctx context.Context, labelID string) error
	// ContentDocumentID reads the Label's content document reference with
	// lock.
	ContentDocumentID(ctx context.Context, labelID string, lock RowLock) (*uuid.UUID, error)
	// SourceLocale reads the Label's source locale with lock.
	SourceLocale(ctx context.Context, labelID string, lock RowLock) (string, error)
	// DocumentRoot reads the Label root of a content mutation with lock.
	DocumentRoot(ctx context.Context, labelID string, lock RowLock) (DocumentRoot, error)
	// DocumentMetadata reads the metadata a collaboration room edits.
	DocumentMetadata(ctx context.Context, labelID string) (DocumentMetadata, error)
	// UpdateDocumentMetadata stores the changed metadata columns and
	// updated_at.
	UpdateDocumentMetadata(ctx context.Context, labelID string, change entityparticipant.MetadataChange, now time.Time) error
	// CheckResourceRoute requires that no route uses the Label slug, and
	// LockResourceRoute locks the route first.
	CheckResourceRoute(ctx context.Context, slug string) error
	LockResourceRoute(ctx context.Context, slug string) error
	// ResourceRouteAvailable reports whether no route uses the Label slug.
	ResourceRouteAvailable(ctx context.Context, slug string) (bool, error)
}

// LabelLocales reads and writes the label_translation table.
type LabelLocales interface {
	// LocaleTitles lists every locale of the Label, ordered by locale.
	LocaleTitles(ctx context.Context, labelID string) ([]LocaleTitle, error)
	// LocaleTitle reads one locale, locked for update when forUpdate is
	// set, and reports whether it exists.
	LocaleTitle(ctx context.Context, labelID, locale string, forUpdate bool) (LocaleTitle, bool, error)
	// LocaleTitleText reads one locale's title. A missing locale is
	// ErrLocaleNotFound.
	LocaleTitleText(ctx context.Context, labelID, locale string) (string, error)
	// SaveLocaleTitle stores the locale's title, creating the locale.
	SaveLocaleTitle(ctx context.Context, labelID, locale, title string, now time.Time) error
	// CreateTargetLocale creates an untitled target locale and reports
	// whether it did.
	CreateTargetLocale(ctx context.Context, labelID, locale string, now time.Time) (bool, error)
	// TouchTargetLocale moves the locale's updated_at and reports whether
	// it exists.
	TouchTargetLocale(ctx context.Context, labelID, locale string, updatedAt time.Time) (bool, error)
	// DeleteTargetLocale deletes the locale and reports whether it existed.
	DeleteTargetLocale(ctx context.Context, labelID, locale string) (bool, error)
}

// TargetMetadataEffect writes the Label locale row of a target-locale
// content batch, in its transaction.
type TargetMetadataEffect func(ctx context.Context, store Store, contentChanged bool) (contentblock.MetadataEffect, error)

// MetadataEffect writes the Label metadata of a revision advance, in its
// transaction.
type MetadataEffect func(ctx context.Context, store Store) (contentblock.MetadataEffect, error)

// ContentDocuments is the Label's content document in the shared content
// store (contentblock.Store), bound to the Label transaction. Each fence
// runs before the content rows are locked.
type ContentDocuments interface {
	CreateContentDocument(ctx context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error)
	// LockedContentSnapshot share-locks and reads the document in the
	// transaction, after the caller locked the Label root.
	LockedContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	// ContentSnapshot reads the document in one snapshot of its own.
	ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	LockContentDocument(ctx context.Context, documentID uuid.UUID) error
	CurrentContentRevision(ctx context.Context, documentID uuid.UUID) (uuid.UUID, error)
	ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence ContentFence) (contentblock.Result, error)
	ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence ContentFence, effect TargetMetadataEffect) (contentblock.Result, error)
	ReplaceContentSnapshot(ctx context.Context, replace contentblock.ReplaceInput, fence ContentFence) (contentblock.Result, error)
	AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence ContentFence, effect MetadataEffect) (contentblock.AdvanceResult, error)
	DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence ContentFence) error
}

// LabelMedia is the shared File, public asset and OG lifecycle of the
// Label's logos.
type LabelMedia interface {
	LockAttachableFiles(ctx context.Context, fileIDs []string) error
	// BindLogo binds the ready asset of fileID to the variant's slot.
	BindLogo(ctx context.Context, fileID, labelID string, variant LogoVariant) error
	// ReleaseLogoBinding releases the variant's slot binding, and
	// ReleaseLogoBindings both slots.
	ReleaseLogoBinding(ctx context.Context, labelID string, variant LogoVariant) error
	ReleaseLogoBindings(ctx context.Context, labelID string) error
	// ReadyAssets resolves ready public assets by ID, ReadyLogoAssets ready
	// logo assets by source File ID.
	ReadyAssets(ctx context.Context, assetIDs []string) (map[string]*commonv1.AssetRef, error)
	ReadyLogoAssets(ctx context.Context, fileIDs []string) (map[string]*commonv1.AssetRef, error)
	// RequestOG starts the Label's current OG generation and returns its
	// run ID, empty when none started; CancelOG cancels it and releases
	// its image.
	RequestOG(ctx context.Context, labelID, reason string) (string, error)
	CancelOG(ctx context.Context, labelID string) error
}

// LabelCollaboration is the shared collaboration and translation authority
// a Label save checks in its transaction.
type LabelCollaboration interface {
	// ResolveSessionPrincipal resolves a collaboration session's principal;
	// an invalid session is auth.ErrSessionPrincipalInvalid.
	ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error)
	// RequireCurrentContributors requires that the contributors may still
	// save the Label's room.
	RequireCurrentContributors(ctx context.Context, labelID string, contributors []string) error
	// RequireDocumentContributors validates and key-share-locks the
	// contributor Members of a save.
	RequireDocumentContributors(ctx context.Context, contributors []string) error
	// ResolveInitialSourceLocale picks a new Label's source locale.
	ResolveInitialSourceLocale(ctx context.Context, acceptLanguage string) string
}
