package criteria

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/sliceutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// CriteriaConfigInput is a requested criteria filter before validation.
type CriteriaConfigInput struct {
	MemberTagIDs []string
	// AccountRoles are role IDs (policyv1.Role); an empty ID stands for an
	// API role the domain does not support.
	AccountRoles     []string
	CreatedAfter     *time.Time
	CreatedBefore    *time.Time
	ExcludeMemberIDs []string
	// malformedTime names a time bound whose wire value is not a valid
	// timestamp; validation rejects it after the identifier checks.
	malformedTime string
}

// normalizeCriteriaConfig validates a requested filter and returns it with
// canonical, sorted and deduplicated identifiers. A nil input is the empty
// filter.
func normalizeCriteriaConfig(input *CriteriaConfigInput) (Config, error) {
	if input == nil {
		return Config{}, nil
	}
	roles, err := normalizeAccountRoles(input.AccountRoles)
	if err != nil {
		return Config{}, err
	}
	memberTagIDs, err := canonicalUUIDs(input.MemberTagIDs, "member_tag_ids")
	if err != nil {
		return Config{}, err
	}
	excludeMemberIDs, err := canonicalUUIDs(input.ExcludeMemberIDs, "exclude_member_ids")
	if err != nil {
		return Config{}, err
	}
	if input.malformedTime != "" {
		return Config{}, fmt.Errorf("%s is invalid", input.malformedTime)
	}
	result := Config{
		MemberTagIDs: memberTagIDs, AccountRoles: roles,
		CreatedAfter: utcTime(input.CreatedAfter), CreatedBefore: utcTime(input.CreatedBefore),
		ExcludeMemberIDs: excludeMemberIDs,
	}
	return result.sorted(), nil
}

// sorted returns the config with sorted, deduplicated and trimmed
// identifiers, as the Store returns a stored filter.
func (c Config) sorted() Config {
	c.MemberTagIDs = sliceutil.SortedUniqueTrimmed(c.MemberTagIDs)
	c.AccountRoles = sliceutil.SortedUniqueTrimmed(c.AccountRoles)
	c.ExcludeMemberIDs = sliceutil.SortedUniqueTrimmed(c.ExcludeMemberIDs)
	return c
}

func normalizeAccountRoles(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, role := range values {
		switch role {
		case policyv1.Role.Admin().ID(), policyv1.Role.Author().ID(), policyv1.Role.User().ID():
		default:
			return nil, fmt.Errorf("account_roles contains an unsupported role")
		}
		if _, exists := seen[role]; exists {
			continue
		}
		seen[role] = struct{}{}
		result = append(result, role)
	}
	sort.Strings(result)
	return result, nil
}

func utcTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	parsed := value.UTC()
	return &parsed
}

func canonicalUUIDs(values []string, field string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, rawValue := range values {
		parsed, err := uuid.Parse(strings.TrimSpace(rawValue))
		if err != nil {
			return nil, fmt.Errorf("%s contains an invalid UUID", field)
		}
		value := parsed.String()
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

// ValidateConfig validates a normalized filter for a criteria type.
func ValidateConfig(kind Kind, config Config) error {
	switch kind.trimmed() {
	case KindAllMembers:
		if len(config.MemberTagIDs) > 0 ||
			len(config.AccountRoles) > 0 ||
			config.CreatedAfter != nil ||
			config.CreatedBefore != nil ||
			len(config.ExcludeMemberIDs) > 0 {
			return fmt.Errorf("all-members Member criteria do not accept filters")
		}
	case KindMemberTags:
		if len(config.MemberTagIDs) == 0 {
			return fmt.Errorf("member-tag Member criteria require member_tag_ids")
		}
		if len(config.AccountRoles) > 0 ||
			config.CreatedAfter != nil ||
			config.CreatedBefore != nil ||
			len(config.ExcludeMemberIDs) > 0 {
			return fmt.Errorf("member-tag Member criteria only accept member_tag_ids")
		}
	case KindFilter:
		if len(config.MemberTagIDs) > 0 {
			return fmt.Errorf("member-filter Member criteria do not accept tag ids")
		}
	default:
		return fmt.Errorf("unknown criteria type: %s", kind)
	}
	if config.CreatedAfter != nil && config.CreatedBefore != nil && config.CreatedAfter.After(*config.CreatedBefore) {
		return fmt.Errorf("created_after must not be later than created_before")
	}
	return nil
}

// replaceConfig stores config as the criteria's filter once every excluded
// Member is an onboarded Member with an exact Identity link.
func replaceConfig(ctx context.Context, store Store, criteriaID string, config Config) error {
	if len(config.ExcludeMemberIDs) > 0 {
		eligible, err := store.EligibleMemberIDs(ctx, config.ExcludeMemberIDs)
		if err != nil {
			return err
		}
		if len(eligible) != len(config.ExcludeMemberIDs) {
			return invalidCriteria("every excluded member must be an onboarded Member with an exact Identity link")
		}
	}
	return store.ReplaceConfig(ctx, criteriaID, config)
}
