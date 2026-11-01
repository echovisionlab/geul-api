package public

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
)

// Store is what the public Terms and Privacy reads read, bound to one read
// snapshot. legalpublicpostgres implements it.
type Store interface {
	// PublishedVersion reads an active or archived version and reports
	// whether it exists.
	PublishedVersion(ctx context.Context, kind legaldomain.PolicyKind, id string) (Version, bool, error)
	// VersionInStatus reads a version in status and reports whether it
	// exists.
	VersionInStatus(ctx context.Context, kind legaldomain.PolicyKind, id string, status legaldomain.Status) (Version, bool, error)
	// EffectiveVersion reads the newest active version in effect at now and
	// reports whether one exists.
	EffectiveVersion(ctx context.Context, kind legaldomain.PolicyKind, now time.Time) (Version, bool, error)
	// NextScheduledVersion reads the scheduled version that takes effect
	// first after now and reports whether one exists.
	NextScheduledVersion(ctx context.Context, kind legaldomain.PolicyKind, now time.Time) (Version, bool, error)
	// CountArchived counts the archived versions; ListArchived lists one
	// page of them, latest effective first.
	CountArchived(ctx context.Context, kind legaldomain.PolicyKind) (int64, error)
	ListArchived(ctx context.Context, kind legaldomain.PolicyKind, limit, offset int32) ([]Version, error)
	// ShareLink reads the unexpired Share Link of token on the version and
	// reports whether it exists.
	ShareLink(ctx context.Context, token, entityType, entityID string, now time.Time) (ShareLink, bool, error)
	// Lifecycle reads the status and effective time of a version and
	// reports whether it exists.
	Lifecycle(ctx context.Context, kind legaldomain.PolicyKind, id string) (legaldomain.Lifecycle, bool, error)
	// DocumentRoot reads the version's content document reference and
	// source locale. A missing version is legaldomain.ErrPolicyNotFound.
	DocumentRoot(ctx context.Context, kind legaldomain.PolicyKind, id string) (DocumentRoot, error)
	// ContentSnapshot reads the document, share-locking its rows; it is
	// called under Units.ReadLocked.
	ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
}

// Units are the read snapshots of the public reads;
// mutation.UnitOfWork[Store] satisfies it.
type Units interface {
	Read(ctx context.Context, read func(Store) error) error
	ReadLocked(ctx context.Context, read func(Store) error) error
}

// Version is a version row as the public reads see it.
type Version struct {
	ID             string
	Version        int
	Title          string
	Status         legaldomain.Status
	EffectiveFrom  *time.Time
	EffectiveUntil *time.Time
}

// DocumentRoot is a version's content document reference.
type DocumentRoot struct {
	ContentDocumentID *uuid.UUID
	SourceLocale      string
}

// ShareLink is the Share Link of a version.
type ShareLink struct {
	ExpiresAt    *time.Time
	PasswordHash *string
}

// Localizer picks the locale a reader sees. It reads outside any snapshot,
// because resolving a selection may store the default localization
// settings.
type Localizer interface {
	Resolve(ctx context.Context, kind legaldomain.PolicyKind, entityID, acceptLanguage string) (publiccontent.Selection, error)
	FallbackToSource(ctx context.Context, kind legaldomain.PolicyKind, entityID string, selection publiccontent.Selection) (publiccontent.Selection, error)
}

// OGDisposition is the state of a locale's OG image of the public route.
type OGDisposition uint8

const (
	OGUnavailable OGDisposition = iota
	OGPending
	OGReady
)

// OGImages reads the OG images of the kind's public route.
type OGImages interface {
	LocalizedOGDisposition(ctx context.Context, kind legaldomain.PolicyKind, locale string) (OGDisposition, error)
	// ReadyLocalizedOGImage reads the ready image of locale, nil when none.
	ReadyLocalizedOGImage(ctx context.Context, kind legaldomain.PolicyKind, locale string) (*Asset, error)
}

// AssetDisposition is how a client presents an asset.
type AssetDisposition uint8

const (
	DispositionUnspecified AssetDisposition = iota
	DispositionInline
	DispositionAttachment
)

// Asset is a ready public image asset.
type Asset struct {
	ID               string
	URL              string
	Extension        string
	MIMEType         string
	FileSize         int64
	SHA256           []byte
	Disposition      AssetDisposition
	DownloadFilename *string
}
