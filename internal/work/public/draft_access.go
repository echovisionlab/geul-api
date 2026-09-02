package public

import (
	"context"
	"errors"
	"fmt"
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

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func hasDraftWorkView(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	workID string,
) (bool, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.Banned || strings.TrimSpace(user.IdentityID.String()) == "" {
		return false, nil
	}
	can, err := policyv1.Work.View(workID)
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, err
	}
	return spiceDB.Can(ctx, decision)
}

func requireDraftShareLinkAccess(
	ctx context.Context,
	db *gorm.DB,
	shareToken string,
	sharePassword string,
	entityType managev1.ShareLinkEntityType,
	entityID string,
	entityName string,
) (*model.ShareLink, error) {
	if shareToken == "" {
		return nil, errs.NotFoundMsg(entityName + " not found")
	}
	var link model.ShareLink
	err := db.WithContext(ctx).
		Where("token = ? AND entity_type = ? AND entity_id = ? AND expires_at > ?", shareToken, entityType.String(), entityID, time.Now()).
		Take(&link).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.NotFoundMsg(entityName + " not found")
	}
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("validate %s share link: %w", entityName, err))
	}
	if link.PasswordHash == nil {
		return &link, nil
	}
	if sharePassword == "" {
		return nil, errs.NotFoundMsg(entityName + " not found")
	}
	match, err := crypto.NewPasswordHasher(nil).Verify(sharePassword, *link.PasswordHash)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("validate %s share link: %w", entityName, err))
	}
	if !match {
		return nil, errs.NotFoundMsg(entityName + " not found")
	}
	return &link, nil
}
