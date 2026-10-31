package sharelinkadapter

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
)

// LegalPreviewLinks writes the Share Links of legal policy versions for the
// Legal domain.
type LegalPreviewLinks struct{}

var _ legaldomain.PreviewLinks = LegalPreviewLinks{}

// CreateAutomaticPreview stores the password-free preview link a scheduled
// update notice mails out. A retry finds the link it already stored; a token
// bound to any other link fails.
func (LegalPreviewLinks) CreateAutomaticPreview(ctx context.Context, tx *gorm.DB, preview legaldomain.AutomaticPreview) error {
	expiresAt := preview.ExpiresAt.UTC()
	link := model.ShareLink{
		Token:      preview.Token,
		EntityType: preview.EntityType,
		EntityID:   preview.EntityID,
		ExpiresAt:  &expiresAt,
		CreatedAt:  preview.CreatedAt.UTC(),
	}
	if err := tx.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "token"}}, DoNothing: true}).
		Create(&link).Error; err != nil {
		return err
	}
	var persisted model.ShareLink
	if err := tx.WithContext(ctx).Where("token = ?", preview.Token).Take(&persisted).Error; err != nil {
		return err
	}
	if persisted.EntityType != preview.EntityType || persisted.EntityID != preview.EntityID ||
		persisted.PasswordHash != nil || persisted.ExpiresAt == nil ||
		!persisted.ExpiresAt.Equal(expiresAt) {
		return fmt.Errorf("automatic legal preview token is already bound to a different share link")
	}
	return nil
}

// DeleteAutomaticPreview deletes the password-free link of token on the
// version.
func (LegalPreviewLinks) DeleteAutomaticPreview(ctx context.Context, tx *gorm.DB, token, entityType, entityID string) error {
	return tx.WithContext(ctx).
		Where("token = ? AND entity_type = ? AND entity_id = ? AND password_hash IS NULL", token, entityType, entityID).
		Delete(&model.ShareLink{}).Error
}

// DeleteForEntity deletes every link of the version.
func (LegalPreviewLinks) DeleteForEntity(ctx context.Context, tx *gorm.DB, entityType, entityID string) error {
	return tx.WithContext(ctx).Where("entity_type = ? AND entity_id = ?", entityType, entityID).Delete(&model.ShareLink{}).Error
}
