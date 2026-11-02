package pageaccess

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// PermissionChecker reads the current account's authoritative role permissions.
type PermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
}

// Audience is what a Page audience decision reads about the reader, bound to
// the transaction of the read it gates. pageaccesspostgres implements it.
type Audience interface {
	// LockActivePrincipal locks the principal's Identity through the
	// identity fence and reports whether it is still active, onboarded and
	// unbanned.
	LockActivePrincipal(ctx context.Context, principal *auth.UserInfo) (bool, error)
	// HasUserTag reports whether the Member carries one of tagIDs.
	HasUserTag(ctx context.Context, memberID string, tagIDs []string) (bool, error)
	// SubscribesToNewsletter reports whether the Identity subscribes to the
	// newsletter.
	SubscribesToNewsletter(ctx context.Context, identityID string) (bool, error)
}

// Reason is why a Page audience decision admits or refuses the reader. The
// values are those of commonv1.PageAccessReason.
type Reason int32

const (
	ReasonUnspecified            Reason = 0
	ReasonAllowed                Reason = 1
	ReasonAuthenticationRequired Reason = 2
	ReasonConditionsNotMet       Reason = 3
)

// PublicSQL is the anonymous audience filter for Page discovery projections.
const PublicSQL = "COALESCE(access_policy->>'mode', '') IN ('', 'PAGE_ACCESS_MODE_UNSPECIFIED', 'PAGE_ACCESS_MODE_PUBLIC')"

// Evaluate checks the Page audience before any document or media is projected.
// Managers retain access to the content they administer. Within role and tag
// groups any selected item qualifies; Match combines the enabled groups.
func Evaluate(ctx context.Context, audience Audience, checker PermissionChecker, pageID string, raw json.RawMessage) (Reason, error) {
	policy, err := Decode(raw)
	if err != nil {
		return ReasonUnspecified, err
	}
	if policy.Mode == ModePublic {
		return ReasonAllowed, nil
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || principal.Banned || principal.IdentityID == "" {
		return ReasonAuthenticationRequired, nil
	}
	active, err := audience.LockActivePrincipal(ctx, principal)
	if err != nil {
		return ReasonUnspecified, err
	}
	if !active {
		return ReasonAuthenticationRequired, nil
	}
	if policy.Mode == ModeAuthenticated {
		return ReasonAllowed, nil
	}
	if checker == nil {
		return ReasonUnspecified, fmt.Errorf("Page access permission checker is required")
	}
	manage, err := policyv1.Page.Manage(pageID)
	if err != nil {
		return ReasonUnspecified, err
	}
	allowed, err := check(ctx, checker, manage)
	if err != nil {
		return ReasonUnspecified, err
	}
	if allowed {
		return ReasonAllowed, nil
	}
	groups, err := conditionGroups(ctx, audience, checker, principal, policy)
	if err != nil {
		return ReasonUnspecified, err
	}
	if matchesGroups(groups, policy.Match) {
		return ReasonAllowed, nil
	}
	return ReasonConditionsNotMet, nil
}

// conditionGroups reports, for each condition group the policy enables,
// whether principal meets it.
func conditionGroups(
	ctx context.Context,
	audience Audience,
	checker PermissionChecker,
	principal *auth.UserInfo,
	policy Policy,
) ([]bool, error) {
	groups := make([]bool, 0, 3)
	if len(policy.AllowedRoles) > 0 {
		matches, err := holdsAnyRole(ctx, checker, policy.AllowedRoles)
		if err != nil {
			return nil, err
		}
		groups = append(groups, matches)
	}
	if len(policy.UserTagIDs) > 0 {
		var matches bool
		if principal.MemberID != "" {
			var err error
			if matches, err = audience.HasUserTag(ctx, principal.MemberID.String(), policy.UserTagIDs); err != nil {
				return nil, err
			}
		}
		groups = append(groups, matches)
	}
	if policy.NewsletterSubscriber {
		matches, err := audience.SubscribesToNewsletter(ctx, principal.IdentityID.String())
		if err != nil {
			return nil, err
		}
		groups = append(groups, matches)
	}
	return groups, nil
}

// holdsAnyRole reports whether the principal holds one of roles.
func holdsAnyRole(ctx context.Context, checker PermissionChecker, roles []policyv1.AuthorizationRole) (bool, error) {
	for _, role := range roles {
		var can policyv1.Can
		var err error
		switch role {
		case policyv1.AuthorizationRole_AUTHOR:
			can, err = policyv1.Platform.IsAuthor()
		case policyv1.AuthorizationRole_ADMIN:
			can, err = policyv1.Platform.IsAdmin()
		}
		if err != nil {
			return false, err
		}
		matches, err := check(ctx, checker, can)
		if err != nil || matches {
			return matches, err
		}
	}
	return false, nil
}

func matchesGroups(groups []bool, match Match) bool {
	for _, value := range groups {
		if match == MatchAll && !value {
			return false
		}
		if match != MatchAll && value {
			return true
		}
	}
	return len(groups) > 0 && match == MatchAll
}

func check(ctx context.Context, checker PermissionChecker, can policyv1.Can) (bool, error) {
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, err
	}
	return checker.Can(ctx, decision)
}

// Require is used by authenticated Page and File reads sharing the same root.
func Require(ctx context.Context, audience Audience, checker PermissionChecker, pageID string, raw json.RawMessage) error {
	reason, err := Evaluate(ctx, audience, checker, pageID, raw)
	if err != nil {
		return failurecause.WithAttrs(err, slog.String("page_id", pageID))
	}
	switch reason {
	case ReasonAllowed:
		return nil
	case ReasonAuthenticationRequired:
		return domainerr.AuthenticationRequired()
	default:
		return domainerr.Denied("you do not have permission to access this page")
	}
}
