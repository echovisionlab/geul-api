package work

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

func resolveVersionContributorNames(
	ctx context.Context,
	db *gorm.DB,
	contributorLists ...[]string,
) (map[string]string, error) {
	all := make([]string, 0)
	for _, contributors := range contributorLists {
		all = append(all, contributors...)
	}
	ids := normalizeContributorMemberIDs(all)
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	var members []model.Member
	if err := db.WithContext(ctx).Select("id", "nickname").Where("id IN ?", []string(ids)).Find(&members).Error; err != nil {
		return nil, fmt.Errorf("resolve work Version contributor Members: %w", err)
	}
	names := make(map[string]string, len(members))
	for _, member := range members {
		name := strings.TrimSpace(member.Nickname)
		if name == "" {
			return nil, fmt.Errorf("work Version contributor Member %s has blank nickname", member.ID)
		}
		names[member.ID] = name
	}
	if len(names) != len(ids) {
		return nil, fmt.Errorf("work Version contributor Member set is incomplete")
	}
	return names, nil
}

func toProtoVersionContributors(
	contributorIDs []string,
	names map[string]string,
) ([]*managev1.VersionContributor, error) {
	ids := normalizeContributorMemberIDs(contributorIDs)
	contributors := make([]*managev1.VersionContributor, 0, len(ids))
	for _, memberID := range ids {
		nickname := strings.TrimSpace(names[memberID])
		if nickname == "" {
			return nil, fmt.Errorf("work Version contributor Member %s is unresolved", memberID)
		}
		contributors = append(contributors, &managev1.VersionContributor{MemberId: memberID, Nickname: nickname})
	}
	return contributors, nil
}
