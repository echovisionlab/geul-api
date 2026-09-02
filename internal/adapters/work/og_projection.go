// Package work adapts Work persistence to shared capabilities.
package work

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

// Projection owns the current OG pointers and bindings for Work.
type Projection struct{}

func NewProjection() *Projection { return &Projection{} }

func (*Projection) Handles(target og.Target) bool {
	switch target.EntityType {
	case "work":
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
	if locale == "" {
		return errs.FailedPrecondition("Work OG target locale is required")
	}
	if err := tx.WithContext(ctx).Table("work_translation").
		Where("entity_id = ? AND locale = ?", target.EntityID, locale).
		Update("og_asset_id", nil).Error; err != nil {
		return err
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).ReleaseExactPublicAssetBindings(
		ctx, target.EntityType, target.EntityID, []string{workOGBindingKey(locale)},
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
	if locale == "" {
		return errs.FailedPrecondition("Work OG target locale is required")
	}
	result := tx.WithContext(ctx).Table("work_translation").
		Where("entity_id = ? AND locale = ?", target.EntityID, locale).
		Updates(structured.Fields{"og_asset_id": assetID, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return og.ErrTranslationTargetMissing
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).BindPublicAsset(ctx, mediaasset.Binding{
		AssetID: assetID, OwnerType: target.EntityType, OwnerID: target.EntityID, BindingKey: workOGBindingKey(locale),
	})
}

func workOGBindingKey(locale string) string { return "og:" + strings.TrimSpace(locale) }

var _ og.Projection = (*Projection)(nil)
