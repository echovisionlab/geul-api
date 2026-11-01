package favicon

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
)

// Stored public asset statuses favicon decisions branch on.
const (
	AssetAllocated     = "allocated"
	AssetReady         = "ready"
	AssetFailed        = "failed"
	AssetDeletePending = "delete_pending"
	AssetDeleted       = "deleted"
)

// DerivativeAsset is a favicon public asset, keyed by the derivative type a
// File's generated set stores it under. Type is empty for a source asset.
type DerivativeAsset struct {
	Type             string
	ID               string
	SourceFileID     *string
	Kind             string
	ObjectKey        string
	Extension        string
	MimeType         string
	FileSize         *int64
	SHA256           []byte
	Disposition      string
	DownloadFilename *string
	Status           string
	CreatedAt        time.Time
}

// Store is the favicon public assets of Files, bound to one transaction.
type Store interface {
	// SourceMimeType returns the MIME type of a source File, or false.
	SourceMimeType(ctx context.Context, fileID string) (string, bool, error)
	// FaviconDerivatives returns a File's favicon derivatives with their
	// assets, by type; a derivative without an asset has an empty ID.
	FaviconDerivatives(ctx context.Context, fileID string) ([]DerivativeAsset, error)
	// NewestReadySourceAsset returns the newest ready favicon asset made
	// directly from a File.
	NewestReadySourceAsset(ctx context.Context, fileID string) (DerivativeAsset, bool, error)
	AssetRef(asset DerivativeAsset) (*mediaasset.Reference, error)

	FaviconDerivativeCount(ctx context.Context, fileID string) (int64, error)
	SourceAssetCount(ctx context.Context, fileID string) (int64, error)
	// LockDeletionAssets locks the favicon derivative assets and source
	// assets of a File FOR UPDATE, in ID order.
	LockDeletionAssets(ctx context.Context, fileID string) ([]DerivativeAsset, error)
	BindingCount(ctx context.Context, assetIDs []string) (int64, error)
	RequestAssetDeletion(ctx context.Context, assetID string) error

	DanglingReadyAssetIDs(ctx context.Context, cutoff time.Time) ([]string, error)
	LockAsset(ctx context.Context, assetID string) (DerivativeAsset, bool, error)
	AssetBound(ctx context.Context, assetID string) (bool, error)
	// MarkAssetDeletePending marks a ready asset for deletion.
	MarkAssetDeletePending(ctx context.Context, assetID string, now time.Time) (bool, error)
}
