package public

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isValidUUID(value string) bool { return uuidPattern.MatchString(value) }

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func uniqueNonEmptyIDs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func hasDraftResourceView(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	resourceID string,
) (bool, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.Banned || strings.TrimSpace(user.IdentityID.String()) == "" {
		return false, nil
	}
	can, err := policyv1.Post.View(resourceID)
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
	validator postdomain.ShareLinkValidator,
) (*model.ShareLink, error) {
	if shareToken == "" {
		return nil, errs.NotFoundMsg(entityName + " not found")
	}
	link, err := validator.ValidateShareLinkForEntity(ctx, db, shareToken, sharePassword, entityType, entityID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.NotFoundMsg(entityName + " not found")
	}
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("validate %s share link: %w", entityName, err))
	}
	return link, nil
}

func resolvedOgAssetRef(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	sourceAssetID *string,
	localizedAssetID *string,
) (*commonv1.AssetRef, error) {
	for _, candidate := range []*string{localizedAssetID, sourceAssetID} {
		if candidate == nil || strings.TrimSpace(*candidate) == "" {
			continue
		}
		var asset model.PublicAsset
		if err := db.WithContext(ctx).
			Where("id = ? AND status = ?", strings.TrimSpace(*candidate), model.PublicAssetStatusReady).
			Take(&asset).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		if asset.FileSize == nil || len(asset.SHA256) != 32 {
			continue
		}
		return mediaasset.NewLifecycle(db, cdnDomain).AssetRef(asset)
	}
	return nil, nil
}
