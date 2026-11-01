package work

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

func resolveVersionContributorNames(
	ctx context.Context,
	store Store,
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
	nicknames, err := store.ContributorNicknames(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("resolve work Version contributor Members: %w", err)
	}
	names := make(map[string]string, len(nicknames))
	for memberID, nickname := range nicknames {
		name := strings.TrimSpace(nickname)
		if name == "" {
			return nil, fmt.Errorf("work Version contributor Member %s has blank nickname", memberID)
		}
		names[memberID] = name
	}
	if len(names) != len(ids) {
		return nil, fmt.Errorf("work Version contributor Member set is incomplete")
	}
	return names, nil
}

func versionContributors(contributorIDs []string, names map[string]string) ([]VersionContributor, error) {
	ids := normalizeContributorMemberIDs(contributorIDs)
	contributors := make([]VersionContributor, 0, len(ids))
	for _, memberID := range ids {
		nickname := strings.TrimSpace(names[memberID])
		if nickname == "" {
			return nil, fmt.Errorf("work Version contributor Member %s is unresolved", memberID)
		}
		contributors = append(contributors, VersionContributor{MemberID: memberID, Nickname: nickname})
	}
	return contributors, nil
}

// normalizeContributorMemberIDs trims, deduplicates and sorts Member IDs.
func normalizeContributorMemberIDs(memberIDs []string) []string {
	unique := make(map[string]struct{}, len(memberIDs))
	for _, memberID := range memberIDs {
		if memberID = strings.TrimSpace(memberID); memberID != "" {
			unique[memberID] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for memberID := range unique {
		result = append(result, memberID)
	}
	slices.Sort(result)
	return result
}
