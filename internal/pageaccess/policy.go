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

// Mode is who may read a Page. The values are those of
// commonv1.PageAccessMode.
type Mode int32

const (
	ModeUnspecified   Mode = 0
	ModePublic        Mode = 1
	ModeAuthenticated Mode = 2
	ModeConditions    Mode = 3
)

// Match combines the condition groups of a ModeConditions policy. The values
// are those of commonv1.PageAccessMatch.
type Match int32

const (
	MatchUnspecified Match = 0
	MatchAny         Match = 1
	MatchAll         Match = 2
)

// Policy is the audience of a Page: anyone, any active account, or the
// accounts that meet its condition groups (a role, a user tag, a newsletter
// subscription).
type Policy struct {
	Mode                 Mode
	Match                Match
	AllowedRoles         []policyv1.AuthorizationRole
	UserTagIDs           []string
	NewsletterSubscriber bool
}

// Normalize returns the canonical persisted policy without changing its input.
func Normalize(policy *Policy) (Policy, error) {
	result := Policy{Mode: ModePublic, Match: MatchAny}
	if policy == nil {
		return result, nil
	}
	if policy.Mode != ModeUnspecified {
		result.Mode = policy.Mode
	}
	switch result.Mode {
	case ModePublic, ModeAuthenticated:
		return result, nil
	case ModeConditions:
	default:
		return Policy{}, fmt.Errorf("unsupported Page access mode")
	}
	if policy.Match != MatchUnspecified {
		result.Match = policy.Match
	}
	if result.Match != MatchAny && result.Match != MatchAll {
		return Policy{}, fmt.Errorf("unsupported Page access match")
	}
	result.AllowedRoles = slices.Clone(policy.AllowedRoles)
	slices.Sort(result.AllowedRoles)
	result.AllowedRoles = slices.Compact(result.AllowedRoles)
	for _, role := range result.AllowedRoles {
		if role != policyv1.AuthorizationRole_AUTHOR && role != policyv1.AuthorizationRole_ADMIN {
			return Policy{}, fmt.Errorf("unsupported Page access role")
		}
	}
	for _, value := range policy.UserTagIDs {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed == uuid.Nil || parsed.String() != value {
			return Policy{}, fmt.Errorf("Page access user tag IDs must be canonical UUIDs")
		}
		result.UserTagIDs = append(result.UserTagIDs, value)
	}
	slices.Sort(result.UserTagIDs)
	result.UserTagIDs = slices.Compact(result.UserTagIDs)
	result.NewsletterSubscriber = policy.NewsletterSubscriber
	if len(result.AllowedRoles) == 0 && len(result.UserTagIDs) == 0 && !result.NewsletterSubscriber {
		return Policy{}, fmt.Errorf("Page access conditions require at least one condition")
	}
	return result, nil
}

// Equal reports whether two normalized policies admit the same audience.
func Equal(left, right Policy) bool {
	return left.Mode == right.Mode && left.Match == right.Match &&
		slices.Equal(left.AllowedRoles, right.AllowedRoles) && slices.Equal(left.UserTagIDs, right.UserTagIDs) &&
		left.NewsletterSubscriber == right.NewsletterSubscriber
}

// Encode produces canonical protobuf JSON for the page.access_policy column.
func Encode(policy Policy) (json.RawMessage, error) {
	normalized, err := Normalize(&policy)
	if err != nil {
		return nil, err
	}
	return protojson.Marshal(Message(normalized))
}

// Decode accepts an empty legacy policy as public and rejects malformed state.
func Decode(raw json.RawMessage) (Policy, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return Normalize(nil)
	}
	var message commonv1.PageAccessPolicy
	if err := protojson.Unmarshal(raw, &message); err != nil {
		return Policy{}, fmt.Errorf("decode Page access policy: %w", err)
	}
	policy := FromMessage(&message)
	return Normalize(&policy)
}

// FromMessage is the policy an API message names; nil names none.
func FromMessage(message *commonv1.PageAccessPolicy) Policy {
	return Policy{
		Mode: Mode(message.GetMode()), Match: Match(message.GetMatch()),
		AllowedRoles: message.GetAllowedRoles(), UserTagIDs: message.GetUserTagIds(),
		NewsletterSubscriber: message.GetNewsletterSubscriber(),
	}
}

// Message is the API message of policy.
func Message(policy Policy) *commonv1.PageAccessPolicy {
	return &commonv1.PageAccessPolicy{
		Mode: commonv1.PageAccessMode(policy.Mode), Match: commonv1.PageAccessMatch(policy.Match),
		AllowedRoles: policy.AllowedRoles, UserTagIds: policy.UserTagIDs,
		NewsletterSubscriber: policy.NewsletterSubscriber,
	}
}
