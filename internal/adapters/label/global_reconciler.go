package label

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// GlobalReconciler removes stale Label OG state before a global regeneration.
// A Label without either logo always falls back to Site OG.
type GlobalReconciler struct{}

func NewGlobalReconciler() *GlobalReconciler { return &GlobalReconciler{} }

func (*GlobalReconciler) ReconcileBeforeGlobalGeneration(
	ctx context.Context,
	tx *gorm.DB,
	cdnDomain string,
) error {
	var labelIDs []string
	if err := tx.WithContext(ctx).Table("label").
		Where("logo_light_file_id IS NULL AND logo_dark_file_id IS NULL").
		Order("id").Pluck("id", &labelIDs).Error; err != nil {
		return err
	}
	for _, labelID := range labelIDs {
		if err := tx.WithContext(ctx).Table("label").Where("id = ?", labelID).Update("og_asset_id", nil).Error; err != nil {
			return err
		}
		if err := og.NewLifecycle(tx, cdnDomain).CancelEntityWithDB(
			ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_LABEL, labelID,
		); err != nil {
			return err
		}
		if err := mediaasset.NewLifecycle(tx, cdnDomain).
			ReleasePublicAssetBindings(ctx, "label", labelID, "og"); err != nil {
			return err
		}
	}
	return nil
}

var _ og.GlobalReconciler = (*GlobalReconciler)(nil)
