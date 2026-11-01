package mediaasset

import (
	"context"
	"time"
)

type CleanupStore interface {
	DueRetiredGenerationIDs(context.Context, time.Time) ([]string, error)
	LockRetiredGeneration(context.Context, string) (Generation, error)
	FileDeletionPending(context.Context, string) (bool, error)
	GenerationReferenced(context.Context, string) (bool, error)
	DeleteGeneration(context.Context, string) (bool, error)
	ReferencedGenerationIDs(context.Context, []string) (map[string]struct{}, error)
	PendingAssets(context.Context) ([]Asset, error)
	AssetBindingCount(context.Context, string) (int64, error)
	MarkAssetDeleted(context.Context, string, time.Time) (bool, error)
	ExpiredUnreadyAssetIDs(context.Context, time.Time) ([]string, error)
	LockExpiredAsset(context.Context, string) (Asset, error)
	AssetHasGeneration(context.Context, string) (bool, error)
	DeleteExpiredAsset(context.Context, string, time.Time) (bool, error)
}

type ActiveTranscodes interface {
	ActiveFileIDs(context.Context, []string) (map[string]struct{}, error)
}
