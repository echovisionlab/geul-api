package post

import (
	"sort"
	"strings"
)

func postTargetRevisionSignal(locale, sourceLocale, revision string) *string {
	if locale == sourceLocale || strings.TrimSpace(revision) == "" {
		return nil
	}
	return &revision
}

// normalizeContributorMemberIDs trims, deduplicates and sorts memberIDs.
func normalizeContributorMemberIDs(memberIDs []string) []string {
	unique := make(map[string]struct{}, len(memberIDs))
	for _, memberID := range memberIDs {
		if memberID = strings.TrimSpace(memberID); memberID != "" {
			unique[memberID] = struct{}{}
		}
	}
	normalized := make([]string, 0, len(unique))
	for memberID := range unique {
		normalized = append(normalized, memberID)
	}
	sort.Strings(normalized)
	return normalized
}
