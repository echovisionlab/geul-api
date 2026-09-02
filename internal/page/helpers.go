package page

import (
	"context"
	"fmt"
	"sort"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func normalizeOptionalNullableString(raw *string) (*string, bool) {
	if raw == nil {
		return nil, false
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil, true
	}
	return &trimmed, true
}

func normalizeStringIDs(ids []string) []string {
	result := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func readyManageOgAssetRef(
	ctx context.Context,
	assets MediaAssets,
	db *gorm.DB,
	candidateIDs ...*string,
) (*commonv1.AssetRef, error) {
	ids := make([]string, 0, len(candidateIDs))
	seen := make(map[string]struct{}, len(candidateIDs))
	for _, candidateID := range candidateIDs {
		if candidateID == nil || strings.TrimSpace(*candidateID) == "" {
			continue
		}
		id := strings.TrimSpace(*candidateID)
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	ready, err := assets.ResolveReadyAssetRefs(ctx, db, ids)
	if err != nil {
		return nil, errs.Internal(err)
	}
	for _, id := range ids {
		if ref := ready[id]; ref != nil {
			return ref, nil
		}
	}
	return nil, nil
}

func normalizeContributorMemberIDs(memberIDs []string) pq.StringArray {
	set := make(map[string]struct{}, len(memberIDs))
	for _, memberID := range memberIDs {
		if memberID = strings.TrimSpace(memberID); memberID != "" {
			set[memberID] = struct{}{}
		}
	}
	result := make(pq.StringArray, 0, len(set))
	for memberID := range set {
		result = append(result, memberID)
	}
	sort.Strings(result)
	return result
}

func resolveVersionContributorNames(ctx context.Context, db *gorm.DB, lists ...[]string) (map[string]string, error) {
	all := make([]string, 0)
	for _, list := range lists {
		all = append(all, list...)
	}
	ids := normalizeContributorMemberIDs(all)
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	var members []model.Member
	if err := db.WithContext(ctx).Select("id", "nickname").Where("id IN ?", []string(ids)).Find(&members).Error; err != nil {
		return nil, fmt.Errorf("resolve page version contributor Members: %w", err)
	}
	names := make(map[string]string, len(members))
	for _, member := range members {
		if strings.TrimSpace(member.Nickname) == "" {
			return nil, fmt.Errorf("page version contributor Member %s has blank nickname", member.ID)
		}
		names[member.ID] = strings.TrimSpace(member.Nickname)
	}
	if len(names) != len(ids) {
		return nil, fmt.Errorf("page version contributor Member set is incomplete")
	}
	return names, nil
}

func toProtoVersionContributors(ids []string, names map[string]string) ([]*managev1.VersionContributor, error) {
	ids = normalizeContributorMemberIDs(ids)
	result := make([]*managev1.VersionContributor, 0, len(ids))
	for _, id := range ids {
		name := strings.TrimSpace(names[id])
		if name == "" {
			return nil, fmt.Errorf("page version contributor Member %s is unresolved", id)
		}
		result = append(result, &managev1.VersionContributor{MemberId: id, Nickname: name})
	}
	return result, nil
}
