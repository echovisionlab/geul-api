// Package label adapts Label persistence to shared capabilities.
package label

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// Projection owns the current OG pointers and bindings for Label.
type Projection struct{}

func NewProjection() *Projection { return &Projection{} }

func (*Projection) Handles(target og.Target) bool {
	switch target.EntityType {
	case "label":
		return true
	default:
		return false
	}
}

func (p *Projection) ReleasePending(ctx context.Context, tx *gorm.DB, target og.Target, cdnDomain string) error {
	if !p.Handles(target) {
		return errs.InvalidEntityType(target.EntityType)
	}
	if err := tx.WithContext(ctx).Table(target.EntityType).
		Where("id = ?", target.EntityID).
		Update("og_asset_id", nil).Error; err != nil {
		return err
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).ReleaseExactPublicAssetBindings(
		ctx, target.EntityType, target.EntityID, []string{"og"},
	)
}

func (p *Projection) Complete(
	ctx context.Context,
	tx *gorm.DB,
	target og.Target,
	assetID string,
	now time.Time,
	cdnDomain string,
) error {
	if !p.Handles(target) {
		return errs.InvalidEntityType(target.EntityType)
	}
	if target.Locale != nil && strings.TrimSpace(*target.Locale) != "" {
		return gorm.ErrRecordNotFound
	}
	result := tx.WithContext(ctx).Table(target.EntityType).Where("id = ?", target.EntityID).
		Updates(structured.Fields{"og_asset_id": assetID, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).BindPublicAsset(ctx, mediaasset.Binding{
		AssetID: assetID, OwnerType: target.EntityType, OwnerID: target.EntityID, BindingKey: "og",
	})
}

var _ og.Projection = (*Projection)(nil)
