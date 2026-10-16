package sharelink

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// ValidateForEntity verifies a bounded ShareLink and its optional Argon2id
// password proof. Invalid, expired, deleted, mismatched, and wrong password
// proofs are intentionally indistinguishable to callers.
func ValidateForEntity(
	ctx context.Context,
	db *gorm.DB,
	token string,
	password string,
	entityType managev1.ShareLinkEntityType,
	entityID string,
) (*model.ShareLink, error) {
	var link model.ShareLink
	if err := db.WithContext(ctx).
		Where("token = ? AND entity_type = ? AND entity_id = ? AND expires_at > ?", token, entityType.String(), entityID, time.Now()).
		Take(&link).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			slog.ErrorContext(ctx, "failed to load share link", "domain", "sharelink", "operation", "validate_for_entity", "outcome", "failed", "entity_type", entityType.String(), "entity_id", entityID, failurecause.Attr(err, token, password))
		}
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
		slog.ErrorContext(ctx, "failed to verify share link password", "domain", "sharelink", "operation", "validate_for_entity", "outcome", "failed", "entity_type", entityType.String(), "entity_id", entityID, failurecause.Attr(err, token, password, *link.PasswordHash))
		return nil, fmt.Errorf("verify share link password: %w", err)
	}
	if !match {
		return nil, gorm.ErrRecordNotFound
	}
	return &link, nil
}
