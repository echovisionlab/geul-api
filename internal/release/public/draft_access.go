package public

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func requireDraftShareLinkAccess(
	ctx context.Context,
	db *gorm.DB,
	shareToken string,
	sharePassword string,
	entityType managev1.ShareLinkEntityType,
	entityID string,
	entityName string,
) (*model.ShareLink, error) {
	notFound := domainerr.New(domainerr.NotFound, "", entityName+" not found")
	if shareToken == "" {
		return nil, notFound
	}
	link, err := validateShareLinkForEntity(
		ctx, db, shareToken, sharePassword, entityType, entityID,
	)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, notFound
	}
	if err != nil {
		return nil, fmt.Errorf("validate %s share link: %w", entityName, err)
	}
	return link, nil
}

func validateShareLinkForEntity(ctx context.Context, db *gorm.DB, token, password string, entityType managev1.ShareLinkEntityType, entityID string) (*model.ShareLink, error) {
	var link model.ShareLink
	if err := db.WithContext(ctx).Where(
		"token = ? AND entity_type = ? AND entity_id = ? AND expires_at > ?",
		token, entityType.String(), entityID, time.Now(),
	).Take(&link).Error; err != nil {
		return nil, err
	}
	if link.PasswordHash == nil {
		return &link, nil
	}
	if password == "" {
		return nil, gorm.ErrRecordNotFound
	}
	match, err := crypto.NewPasswordHasher(nil).Verify(password, *link.PasswordHash)
	if err != nil {
		return nil, fmt.Errorf("verify share link password: %w", err)
	}
	if !match {
		return nil, gorm.ErrRecordNotFound
	}
	return &link, nil
}
