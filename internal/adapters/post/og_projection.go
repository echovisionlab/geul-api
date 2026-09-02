// Package post adapts Post persistence to shared capabilities.
package post

import (
	"context"
	"time"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// Projection owns the current OG pointers and bindings for Post.
type Projection struct{}

func NewProjection() *Projection { return &Projection{} }

func (*Projection) Handles(target og.Target) bool {
	switch target.EntityType {
	case "post":
		return true
	default:
		return false
	}
}

func (p *Projection) ReleasePending(ctx context.Context, tx *gorm.DB, target og.Target, cdnDomain string) error {
	if !p.Handles(target) {
		return errs.InvalidEntityType(target.EntityType)
	}
	locale, err := target.CanonicalLocale()
	if err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Table(target.EntityType+"_translation").
		Where("entity_id = ? AND locale = ?", target.EntityID, locale).
		Update("og_asset_id", nil).Error; err != nil {
		return err
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).ReleaseExactPublicAssetBindings(
		ctx, target.EntityType, target.EntityID, []string{bindingKey(locale)},
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
	locale, err := target.CanonicalLocale()
	if err != nil {
		return err
	}
	if locale != "" {
		result := tx.WithContext(ctx).Table(target.EntityType+"_translation").
			Where("entity_id = ? AND locale = ?", target.EntityID, locale).
			Updates(structured.Fields{"og_asset_id": assetID, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return og.ErrTranslationTargetMissing
		}
	} else {
		result := tx.WithContext(ctx).Table(target.EntityType).
			Where("id = ?", target.EntityID).
			Updates(structured.Fields{"og_asset_id": assetID, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).BindPublicAsset(ctx, mediaasset.Binding{
		AssetID: assetID, OwnerType: target.EntityType, OwnerID: target.EntityID, BindingKey: bindingKey(locale),
	})
}

func bindingKey(locale string) string {
	if locale == "" {
		return "og"
	}
	return "og:" + locale
}

var _ og.Projection = (*Projection)(nil)
