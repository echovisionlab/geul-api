package sharelinkadapter

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	sharelinkpostgres "github.com/echovisionlab/geul-api/internal/sharelink/postgres"
)

// LegalPreviewLinks writes the Share Links of legal policy versions for the
// Legal domain.
type LegalPreviewLinks struct{}

var _ legaldomain.PreviewLinks = LegalPreviewLinks{}

// CreateAutomaticPreview stores the password-free preview link a scheduled
// update notice mails out. A retry finds the link it already stored; a token
// bound to any other link fails.
func (LegalPreviewLinks) CreateAutomaticPreview(ctx context.Context, tx *gorm.DB, preview legaldomain.AutomaticPreview) error {
	links := sharelinkpostgres.NewStore(tx)
	expiresAt := preview.ExpiresAt.UTC()
	if err := links.InsertLinkUnlessTokenExists(ctx, sharelink.Link{
		Token:      preview.Token,
		EntityType: sharelink.EntityType(preview.EntityType),
		EntityID:   preview.EntityID,
		ExpiresAt:  expiresAt,
		CreatedAt:  preview.CreatedAt.UTC(),
	}); err != nil {
		return err
	}
	persisted, err := links.LinkByToken(ctx, preview.Token)
	if err != nil {
		return err
	}
	if persisted.EntityType != sharelink.EntityType(preview.EntityType) || persisted.EntityID != preview.EntityID ||
		persisted.PasswordHash != nil || !persisted.ExpiresAt.Equal(expiresAt) {
		return fmt.Errorf("automatic legal preview token is already bound to a different share link")
	}
	return nil
}

// DeleteAutomaticPreview deletes the password-free link of token on the
// version.
func (LegalPreviewLinks) DeleteAutomaticPreview(ctx context.Context, tx *gorm.DB, token, entityType, entityID string) error {
	return sharelinkpostgres.NewStore(tx).DeletePasswordFreeLink(ctx, token, sharelink.EntityType(entityType), entityID)
}

// DeleteForEntity deletes every link of the version.
func (LegalPreviewLinks) DeleteForEntity(ctx context.Context, tx *gorm.DB, entityType, entityID string) error {
	return sharelinkpostgres.NewStore(tx).DeleteTargetLinks(ctx, entityID, sharelink.EntityType(entityType))
}
