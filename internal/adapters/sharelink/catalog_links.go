package sharelinkadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

// CatalogLinks removes the share links of a deleted catalog entity in the
// deleting transaction.
type CatalogLinks struct{}

// NewCatalogLinks returns the share link cleanup of Labels and Artists.
func NewCatalogLinks() CatalogLinks { return CatalogLinks{} }

// DeleteLabelShareLinks deletes every share link of the Label.
func (CatalogLinks) DeleteLabelShareLinks(ctx context.Context, tx *gorm.DB, labelID string) error {
	return deleteEntityShareLinks(ctx, tx, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_LABEL, labelID)
}

func deleteEntityShareLinks(ctx context.Context, tx *gorm.DB, entityType managev1.ShareLinkEntityType, entityID string) error {
	return tx.WithContext(ctx).
		Where("entity_type = ? AND entity_id = ?", entityType.String(), entityID).
		Delete(&model.ShareLink{}).Error
}
