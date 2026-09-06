// Package release adapts Release persistence to shared capabilities.
package release

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

// Projection preserves completion of Release OG work created before new
// Release generation was disabled. No current request source creates it.
type Projection struct{}

func NewProjection() *Projection { return &Projection{} }

func (*Projection) Handles(target og.Target) bool { return target.EntityType == "release" }

func (p *Projection) ReleasePending(context.Context, *gorm.DB, og.Target, string) error {
	return errs.FailedPrecondition("new Release OG generation is disabled")
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
	result := tx.WithContext(ctx).Table("release").Where("id = ?", target.EntityID).
		Updates(structured.Fields{"og_asset_id": assetID, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).BindPublicAsset(ctx, mediaasset.Binding{
		AssetID: assetID, OwnerType: "release", OwnerID: target.EntityID, BindingKey: "og",
	})
}

var _ og.Projection = (*Projection)(nil)
