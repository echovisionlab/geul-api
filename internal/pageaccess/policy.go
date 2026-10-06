// Package pageaccess owns the persisted Page audience and its read decision.
package pageaccess

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
)

// Normalize returns the canonical persisted policy without changing its input.
func Normalize(policy *commonv1.PageAccessPolicy) (*commonv1.PageAccessPolicy, error) {
	result := &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_PUBLIC, Match: commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ANY}
	if policy == nil {
		return result, nil
	}
	if policy.Mode != commonv1.PageAccessMode_PAGE_ACCESS_MODE_UNSPECIFIED {
		result.Mode = policy.Mode
	}
	switch result.Mode {
	case commonv1.PageAccessMode_PAGE_ACCESS_MODE_PUBLIC, commonv1.PageAccessMode_PAGE_ACCESS_MODE_AUTHENTICATED:
		return result, nil
	case commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS:
	default:
		return nil, fmt.Errorf("unsupported Page access mode")
	}
	if policy.Match != commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_UNSPECIFIED {
		result.Match = policy.Match
	}
	if result.Match != commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ANY && result.Match != commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ALL {
		return nil, fmt.Errorf("unsupported Page access match")
	}
	result.AllowedRoles = slices.Clone(policy.AllowedRoles)
	slices.Sort(result.AllowedRoles)
	result.AllowedRoles = slices.Compact(result.AllowedRoles)
	for _, role := range result.AllowedRoles {
		if role != policyv1.AuthorizationRole_AUTHOR && role != policyv1.AuthorizationRole_ADMIN {
			return nil, fmt.Errorf("unsupported Page access role")
		}
	}
	for _, value := range policy.UserTagIds {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed == uuid.Nil || parsed.String() != value {
			return nil, fmt.Errorf("Page access user tag IDs must be canonical UUIDs")
		}
		result.UserTagIds = append(result.UserTagIds, value)
	}
	slices.Sort(result.UserTagIds)
	result.UserTagIds = slices.Compact(result.UserTagIds)
	result.NewsletterSubscriber = policy.NewsletterSubscriber
	if len(result.AllowedRoles) == 0 && len(result.UserTagIds) == 0 && !result.NewsletterSubscriber {
		return nil, fmt.Errorf("Page access conditions require at least one condition")
	}
	return result, nil
}

// Encode produces canonical protobuf JSON for the page.access_policy column.
func Encode(policy *commonv1.PageAccessPolicy) (json.RawMessage, error) {
	normalized, err := Normalize(policy)
	if err != nil {
		return nil, err
	}
	return protojson.Marshal(normalized)
}

// Decode accepts an empty legacy policy as public and rejects malformed state.
func Decode(raw json.RawMessage) (*commonv1.PageAccessPolicy, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return Normalize(nil)
	}
	var policy commonv1.PageAccessPolicy
	if err := protojson.Unmarshal(raw, &policy); err != nil {
		return nil, fmt.Errorf("decode Page access policy: %w", err)
	}
	return Normalize(&policy)
}
