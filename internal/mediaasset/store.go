package mediaasset

import (
	"context"
	"errors"
	"time"
)

var (
	ErrAssetNotFound      = errors.New("public asset not found")
	ErrGenerationNotFound = errors.New("media generation not found")
	ErrBindingConflict    = errors.New("public asset binding changed concurrently")
)

type AssetStatus string

const (
	AssetAllocated     AssetStatus = "allocated"
	AssetReady         AssetStatus = "ready"
	AssetDeletePending AssetStatus = "delete_pending"
	AssetDeleted       AssetStatus = "deleted"
	AssetFailed        AssetStatus = "failed"
)

type GenerationStatus string

const (
	GenerationAllocated GenerationStatus = "allocated"
	GenerationReady     GenerationStatus = "ready"
	GenerationRetired   GenerationStatus = "retired"
)

type Asset struct {
	ID                string
	SourceFileID      *string
	Kind              string
	ObjectKey         string
	Extension         string
	MimeType          string
	FileSize          *int64
	SHA256            []byte
	Disposition       string
	DownloadFilename  *string
	Status            AssetStatus
	ReadyAt           *time.Time
	DeleteRequestedAt *time.Time
	DeletedAt         *time.Time
	FailedAt          *time.Time
	FailureReason     *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type AssetBinding struct {
	AssetID      string
	OwnerType    string
	OwnerID      string
	BindingKey   string
	SourceFileID *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Generation struct {
	ID             string
	FileID         string
	Kind           string
	ObjectPrefix   string
	ManifestName   string
	ManifestSHA256 []byte
	ObjectCount    *int32
	TotalSize      *int64
	Status         GenerationStatus
	ReadyAt        *time.Time
	RetiredAt      *time.Time
	DeleteAfter    *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Store is bound to the transaction that owns the asset mutation. Locking
// methods acquire rows in ascending ID order before binding rows are locked.
type Store interface {
	LockAttachableFiles(context.Context, []string) error
	InsertAsset(context.Context, Asset) error
	Asset(context.Context, string) (Asset, error)
	LockAsset(context.Context, string) (Asset, error)
	LockAssetForDeletion(context.Context, string) (Asset, error)
	LockAssets(context.Context, []string) (map[string]Asset, error)
	ReadyAssetForFile(context.Context, string, string) (Asset, error)
	ReadyAssetsForFiles(context.Context, string, []string) ([]Asset, error)
	CompleteAsset(context.Context, string, int64, []byte, time.Time) error
	Binding(context.Context, string, string, string) (AssetBinding, bool, error)
	LockBinding(context.Context, string, string, string) (AssetBinding, bool, error)
	InsertBinding(context.Context, AssetBinding) error
	ReplaceBinding(context.Context, AssetBinding, string) (bool, error)
	BindingKeys(context.Context, string, string, string) ([]string, error)
	Bindings(context.Context, string, string, []string) ([]AssetBinding, error)
	LockBindings(context.Context, string, string, []string) ([]AssetBinding, error)
	LockAssetBindings(context.Context, []string) ([]AssetBinding, error)
	DeleteBindings(context.Context, string, string, []string) (int64, error)
	MarkUnboundAssetsDeletePending(context.Context, []string, time.Time) error
	MarkAssetDeletePending(context.Context, string, time.Time) (bool, error)
	Generation(context.Context, string) (Generation, error)
	LockGeneration(context.Context, string) (Generation, error)
	RetireOtherGenerations(context.Context, string, string, time.Time, time.Time) error
	CompleteGeneration(context.Context, string, []byte, int32, int64, time.Time) error
}
