package sharelinkadapter

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// FormLinks removes the share links of a deleted Form in the deleting
// transaction.
type FormLinks struct{}

// DeleteFormShareLinks deletes the Form's submission and dashboard share
// links.
func (FormLinks) DeleteFormShareLinks(ctx context.Context, tx *gorm.DB, formID string) error {
	return tx.WithContext(ctx).
		Where("entity_id = ? AND entity_type IN ?", formID, []string{
			managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM.String(),
			managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD.String(),
		}).
		Delete(&model.ShareLink{}).Error
}

// FindShareLink returns the share link of token, or gorm.ErrRecordNotFound.
func (FormLinks) FindShareLink(ctx context.Context, db *gorm.DB, token string) (*model.ShareLink, error) {
	var link model.ShareLink
	if err := db.WithContext(ctx).First(&link, "token = ?", token).Error; err != nil {
		return nil, err
	}
	return &link, nil
}
