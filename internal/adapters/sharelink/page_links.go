package sharelinkadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

// PageLinks removes the share links of a deleted Page in the deleting
// transaction.
type PageLinks struct{}

// DeletePageShareLinks deletes every share link of the Page, including links
// stored under the legacy "page" entity type.
func (PageLinks) DeletePageShareLinks(ctx context.Context, tx *gorm.DB, pageID string) error {
	return tx.WithContext(ctx).
		Where("entity_id = ? AND entity_type IN ?", pageID, []string{
			"page",
			managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE.String(),
		}).
		Delete(&model.ShareLink{}).Error
}
