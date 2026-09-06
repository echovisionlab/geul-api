package public

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/crypto"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

func hasDraftResourceView(ctx context.Context, spiceDB *auth.SpiceDBClient, resourceID string) (bool, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.Banned || strings.TrimSpace(user.IdentityID.String()) == "" {
		return false, nil
	}
	can, err := policyv1.Artist.View(resourceID)
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, err
	}
	return spiceDB.Can(ctx, decision)
}

func requireOpaqueDraftShareLinkAccess(ctx context.Context, db *gorm.DB, token, password string, entityType managev1.ShareLinkEntityType, entityID, entityName string) error {
	if token == "" {
		return errs.NotFoundMsg(entityName + " not found")
	}
	var link model.ShareLink
	if err := db.WithContext(ctx).Where("token = ? AND entity_type = ? AND entity_id = ? AND expires_at > ?", token, entityType.String(), entityID, time.Now()).Take(&link).Error; err != nil {
		return errs.NotFoundMsg(entityName + " not found")
	}
	if link.PasswordHash == nil {
		return nil
	}
	if password == "" {
		return errs.NotFoundMsg(entityName + " not found")
	}
	match, err := crypto.NewPasswordHasher(nil).Verify(password, *link.PasswordHash)
	if err != nil {
		return errs.NotFoundMsg(entityName + " not found")
	}
	if !match {
		return errs.NotFoundMsg(entityName + " not found")
	}
	return nil
}
