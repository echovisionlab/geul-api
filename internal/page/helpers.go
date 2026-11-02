package page

import (
	"context"
	"fmt"
	"sort"
	"strings"
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

// NormalizeContributorMemberIDs returns the distinct non-blank Member IDs,
// sorted.
func NormalizeContributorMemberIDs(memberIDs []string) []string {
	set := make(map[string]struct{}, len(memberIDs))
	for _, memberID := range memberIDs {
		if memberID = strings.TrimSpace(memberID); memberID != "" {
			set[memberID] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for memberID := range set {
		result = append(result, memberID)
	}
	sort.Strings(result)
	return result
}

// Contributor is a Member credited on a Page version.
type Contributor struct {
	MemberID string
	Nickname string
}

func (s *Service) resolveVersionContributorNames(ctx context.Context, lists ...[]string) (map[string]string, error) {
	all := make([]string, 0)
	for _, list := range lists {
		all = append(all, list...)
	}
	ids := NormalizeContributorMemberIDs(all)
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	var nicknames map[string]string
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		nicknames, err = store.Nicknames(ctx, ids)
		return err
	}); err != nil {
		return nil, fmt.Errorf("resolve page version contributor Members: %w", err)
	}
	names := make(map[string]string, len(nicknames))
	for memberID, nickname := range nicknames {
		if strings.TrimSpace(nickname) == "" {
			return nil, fmt.Errorf("page version contributor Member %s has blank nickname", memberID)
		}
		names[memberID] = strings.TrimSpace(nickname)
	}
	if len(names) != len(ids) {
		return nil, fmt.Errorf("page version contributor Member set is incomplete")
	}
	return names, nil
}

func versionContributors(ids []string, names map[string]string) ([]Contributor, error) {
	ids = NormalizeContributorMemberIDs(ids)
	result := make([]Contributor, 0, len(ids))
	for _, id := range ids {
		name := strings.TrimSpace(names[id])
		if name == "" {
			return nil, fmt.Errorf("page version contributor Member %s is unresolved", id)
		}
		result = append(result, Contributor{MemberID: id, Nickname: name})
	}
	return result, nil
}
