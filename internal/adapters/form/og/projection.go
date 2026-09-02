package formog

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

// Projection owns Form's locale-scoped current OG pointer and public binding.
type Projection struct{}

func NewProjection() *Projection { return &Projection{} }

func (*Projection) Handles(target og.Target) bool { return target.EntityType == "form" }

func (p *Projection) ReleasePending(ctx context.Context, tx *gorm.DB, target og.Target, cdnDomain string) error {
	if !p.Handles(target) {
		return errs.InvalidEntityType(target.EntityType)
	}
	locale := formOgLocale(target.Locale)
	if err := tx.WithContext(ctx).Table("form_translation").
		Where("entity_id = ? AND locale = ?", target.EntityID, locale).
		Update("og_asset_id", nil).Error; err != nil {
		return err
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).ReleaseExactPublicAssetBindings(
		ctx, "form", target.EntityID, []string{formOgBindingKey(locale)},
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
	locale := formOgLocale(target.Locale)
	if locale == "" {
		return gorm.ErrRecordNotFound
	}
	result := tx.WithContext(ctx).Table("form_translation").
		Where("entity_id = ? AND locale = ?", target.EntityID, locale).
		Updates(structured.Fields{"og_asset_id": assetID, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return og.ErrTranslationTargetMissing
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).BindPublicAsset(ctx, mediaasset.Binding{
		AssetID: assetID, OwnerType: "form", OwnerID: target.EntityID, BindingKey: formOgBindingKey(locale),
	})
}

func formOgLocale(locale *string) string {
	if locale == nil {
		return ""
	}
	return strings.TrimSpace(*locale)
}

func formOgBindingKey(locale string) string {
	if locale == "" {
		return "og"
	}
	return "og:" + locale
}

var _ og.Projection = (*Projection)(nil)
