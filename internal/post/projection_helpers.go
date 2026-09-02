package post

import (
	"context"
	"fmt"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

func nullableStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func loadReadyManageOgAssetRefs(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	candidateIDs ...*string,
) (map[string]*commonv1.AssetRef, error) {
	assetIDs := normalizedManageOgAssetIDs(candidateIDs)
	ready := make(map[string]*commonv1.AssetRef, len(assetIDs))
	if len(assetIDs) == 0 {
		return ready, nil
	}
	var assets []model.PublicAsset
	if err := db.WithContext(ctx).
		Where("id IN ? AND status = ?", assetIDs, model.PublicAssetStatusReady).
		Find(&assets).Error; err != nil {
		return nil, errs.Internal(err)
	}
	lifecycle := mediaasset.NewLifecycle(db, cdnDomain)
	for _, asset := range assets {
		if asset.FileSize == nil || len(asset.SHA256) != 32 {
			continue
		}
		ref, err := lifecycle.AssetRef(asset)
		if err != nil {
			return nil, err
		}
		ready[asset.ID] = ref
	}
	return ready, nil
}

func manageOgAssetFromReadyMap(ready map[string]*commonv1.AssetRef, candidateIDs ...*string) *commonv1.AssetRef {
	for _, candidateID := range candidateIDs {
		if candidateID == nil {
			continue
		}
		if assetID := strings.TrimSpace(*candidateID); assetID != "" && ready[assetID] != nil {
			return ready[assetID]
		}
	}
	return nil
}

func readyManageOgAssetRef(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	candidateIDs ...*string,
) (*commonv1.AssetRef, error) {
	ready, err := loadReadyManageOgAssetRefs(ctx, db, cdnDomain, candidateIDs...)
	if err != nil {
		return nil, err
	}
	return manageOgAssetFromReadyMap(ready, candidateIDs...), nil
}

func normalizedManageOgAssetIDs(candidateIDs []*string) []string {
	seen := make(map[string]struct{}, len(candidateIDs))
	assetIDs := make([]string, 0, len(candidateIDs))
	for _, candidateID := range candidateIDs {
		if candidateID == nil {
			continue
		}
		assetID := strings.TrimSpace(*candidateID)
		if assetID == "" {
			continue
		}
		if _, ok := seen[assetID]; ok {
			continue
		}
		seen[assetID] = struct{}{}
		assetIDs = append(assetIDs, assetID)
	}
	return assetIDs
}

func resolveVersionContributorNames(
	ctx context.Context,
	db *gorm.DB,
	contributorLists ...[]string,
) (map[string]string, error) {
	allIDs := make([]string, 0)
	for _, contributorIDs := range contributorLists {
		allIDs = append(allIDs, contributorIDs...)
	}
	normalized := normalizeContributorMemberIDs(allIDs)
	if len(normalized) == 0 {
		return map[string]string{}, nil
	}
	var members []model.Member
	if err := db.WithContext(ctx).Select("id", "nickname").Where("id IN ?", []string(normalized)).Find(&members).Error; err != nil {
		return nil, fmt.Errorf("resolve post version contributor Members: %w", err)
	}
	names := make(map[string]string, len(members))
	for i := range members {
		if nickname := strings.TrimSpace(members[i].Nickname); nickname != "" {
			names[members[i].ID] = nickname
		}
	}
	if len(names) != len(normalized) {
		return nil, fmt.Errorf("post version contributor Member set is incomplete")
	}
	return names, nil
}

func toProtoVersionContributors(contributorIDs []string, names map[string]string) ([]*managev1.VersionContributor, error) {
	normalized := normalizeContributorMemberIDs(contributorIDs)
	contributors := make([]*managev1.VersionContributor, 0, len(normalized))
	for _, memberID := range normalized {
		nickname := strings.TrimSpace(names[memberID])
		if nickname == "" {
			return nil, fmt.Errorf("post version contributor Member %s is unresolved", memberID)
		}
		contributors = append(contributors, &managev1.VersionContributor{MemberId: memberID, Nickname: nickname})
	}
	return contributors, nil
}
