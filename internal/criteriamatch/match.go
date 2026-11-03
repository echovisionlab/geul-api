// Package criteriamatch decides whether a Member matches a Member criteria.
// Restricted downloads and sign-in apps both admit Members through it, so a
// criteria selects the same Members wherever it grants access. Callers load
// the criteria and the Member's facts; this package only decides.
package criteriamatch

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
)

// Kind is a stored Member criteria kind.
type Kind string

const (
	KindAllMembers Kind = "MEMBER_CRITERIA_KIND_ALL_MEMBERS"
	KindMemberTags Kind = "MEMBER_CRITERIA_KIND_MEMBER_TAGS"
	KindFilter     Kind = "MEMBER_CRITERIA_KIND_FILTER"
)

// Config is the stored selection of a Member criteria.
type Config struct {
	MemberTagIDs     []string `json:"member_tag_ids,omitempty"`
	AccountRoles     []string `json:"account_roles,omitempty"`
	CreatedAfter     *time.Time
	CreatedBefore    *time.Time
	ExcludeMemberIDs []string `json:"exclude_member_ids,omitempty"`
}

// Criteria is a Member criteria as matching reads it.
type Criteria struct {
	ID     string
	Kind   Kind
	Config Config
}

// Member is the active Member being matched.
type Member struct {
	ID        string
	CreatedAt time.Time
}

// Facts are what matching reads beyond the criteria: which of the referenced
// Member tags still exist and which of them the Member has, and the platform
// roles the Member holds (RequestedRoles names the ones to resolve).
type Facts struct {
	ExistingTagIDs map[string]struct{}
	MemberTagIDs   map[string]struct{}
	Roles          map[policyv1.RoleID]bool
}

// Matches reports whether member is selected by criteria. A criteria with an
// unknown kind or a malformed selection selects nobody.
func Matches(member Member, criteria Criteria, facts Facts) bool {
	switch Kind(strings.TrimSpace(string(criteria.Kind))) {
	case KindAllMembers:
		return true
	case KindMemberTags:
		return matchesTags(criteria.Config, facts)
	case KindFilter:
		return matchesFilter(member, criteria.Config, facts.Roles)
	default:
		return false
	}
}

func matchesTags(config Config, facts Facts) bool {
	if len(config.AccountRoles) > 0 || config.CreatedAfter != nil || config.CreatedBefore != nil || len(config.ExcludeMemberIDs) > 0 {
		return false
	}
	tagIDs, valid := ValidUUIDSet(config.MemberTagIDs)
	if !valid || len(tagIDs) == 0 {
		return false
	}
	for _, tagID := range tagIDs {
		if _, exists := facts.ExistingTagIDs[tagID]; !exists {
			return false
		}
	}
	for _, tagID := range tagIDs {
		if _, exists := facts.MemberTagIDs[tagID]; exists {
			return true
		}
	}
	return false
}

func matchesFilter(member Member, config Config, roles map[policyv1.RoleID]bool) bool {
	if len(config.MemberTagIDs) > 0 {
		return false
	}
	requested, err := NormalizeRoles(config.AccountRoles)
	if err != nil {
		// RequestedRoles already failed the caller for these stored roles;
		// never match on them.
		return false
	}
	if len(requested) > 0 && !slices.ContainsFunc(requested, func(role policyv1.RoleID) bool { return roles[role] }) {
		return false
	}
	if config.CreatedAfter != nil && member.CreatedAt.Before(*config.CreatedAfter) {
		return false
	}
	if config.CreatedBefore != nil && member.CreatedAt.After(*config.CreatedBefore) {
		return false
	}
	excludedMemberIDs, valid := ValidUUIDSet(config.ExcludeMemberIDs)
	return valid && !slices.Contains(excludedMemberIDs, member.ID)
}

// ReferencedTagIDs returns the valid Member tag IDs the tag criteria in
// criteriaSet select, sorted and unique: the tags whose existence and
// assignment Facts must report.
func ReferencedTagIDs(criteriaSet []Criteria) []string {
	referenced := make([]string, 0)
	for _, criteria := range criteriaSet {
		if Kind(strings.TrimSpace(string(criteria.Kind))) != KindMemberTags {
			continue
		}
		if tagIDs, valid := ValidUUIDSet(criteria.Config.MemberTagIDs); valid {
			referenced = append(referenced, tagIDs...)
		}
	}
	return sortedUnique(referenced)
}

// RequestedRoles returns the platform roles the filter criteria in
// criteriaSet test, for Facts.Roles. A stored role it cannot parse is an
// error of the stored criteria, not of the request being decided.
func RequestedRoles(criteriaSet []Criteria) ([]policyv1.RoleID, error) {
	requested := make(map[policyv1.RoleID]struct{})
	for _, criteria := range criteriaSet {
		if Kind(strings.TrimSpace(string(criteria.Kind))) != KindFilter {
			continue
		}
		roles, err := NormalizeRoles(criteria.Config.AccountRoles)
		if err != nil {
			return nil, fmt.Errorf("read stored Member criteria %s: %w", criteria.ID, err)
		}
		for _, role := range roles {
			requested[role] = struct{}{}
		}
	}
	roles := make([]policyv1.RoleID, 0, len(requested))
	for role := range requested {
		roles = append(roles, role)
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].ID() < roles[j].ID() })
	return roles, nil
}

// PlatformCan is the platform permission that proves a Member holds role.
func PlatformCan(role policyv1.RoleID) (policyv1.Can, error) {
	switch role {
	case policyv1.Role.Admin():
		return policyv1.Platform.IsAdmin()
	case policyv1.Role.Author():
		return policyv1.Platform.IsAuthor()
	case policyv1.Role.User():
		return policyv1.Platform.IsUser()
	default:
		return policyv1.Can{}, fmt.Errorf("unsupported Member criteria account role %q", role.ID())
	}
}

// NormalizeRoles parses stored account roles, dropping blanks and duplicates.
func NormalizeRoles(values []string) ([]policyv1.RoleID, error) {
	seen := make(map[policyv1.RoleID]struct{}, len(values))
	roles := make([]policyv1.RoleID, 0, len(values))
	for _, value := range values {
		normalized := strings.TrimSpace(strings.ToLower(value))
		if normalized == "" {
			continue
		}
		role, ok := policyv1.Role.Parse(normalized)
		if !ok {
			return nil, fmt.Errorf("invalid account permission %q", value)
		}
		if _, exists := seen[role]; exists {
			continue
		}
		seen[role] = struct{}{}
		roles = append(roles, role)
	}
	return roles, nil
}

// ValidUUIDSet returns values trimmed, sorted and unique, or false when one
// is not a UUID.
func ValidUUIDSet(values []string) ([]string, bool) {
	result := make([]string, 0, len(values))
	for _, rawValue := range values {
		value := strings.TrimSpace(rawValue)
		if _, err := uuid.Parse(value); err != nil {
			return nil, false
		}
		result = append(result, value)
	}
	return sortedUnique(result), true
}

func sortedUnique(values []string) []string {
	sort.Strings(values)
	return slices.Compact(values)
}
