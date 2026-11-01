package domain

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
)

type SourcePromotionStore interface {
	ReadyPromotionReferenceForFile(context.Context, string, string, string) (*mediaasset.Reference, error)
	PromotionFile(context.Context, string, bool) (File, error)
	SourcePromotionAsset(context.Context, string, bool) (mediaasset.Asset, error)
	PromotionAsset(context.Context, string, bool) (mediaasset.Asset, error)
	ResumePromotionAsset(context.Context, string, bool, time.Time) (bool, error)
	FailPromotionAsset(context.Context, string, string, time.Time) (bool, error)
	AllocatePromotionAsset(context.Context, string, mediaasset.Allocation) (*mediaasset.Asset, *mediaasset.WriteTarget, error)
	CompletePromotionAsset(context.Context, string, mediaasset.WrittenAsset) error
	ReadyPromotionReference(context.Context, string, string) (*mediaasset.Reference, error)
}
