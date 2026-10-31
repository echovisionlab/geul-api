package pageaccess

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

// PermissionChecker reads the current account's authoritative role permissions.
type PermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
}

// PublicSQL is the anonymous audience filter for Page discovery projections.
const PublicSQL = "COALESCE(access_policy->>'mode', '') IN ('', 'PAGE_ACCESS_MODE_UNSPECIFIED', 'PAGE_ACCESS_MODE_PUBLIC')"

// Evaluate checks the Page audience before any document or media is projected.
// Managers retain access to the content they administer. Within role and tag
// groups any selected item qualifies; Match combines the enabled groups.
func Evaluate(ctx context.Context, db *gorm.DB, checker PermissionChecker, pageID string, raw json.RawMessage) (commonv1.PageAccessReason, error) {
	policy, err := Decode(raw)
	if err != nil {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_UNSPECIFIED, err
	}
	if policy.Mode == commonv1.PageAccessMode_PAGE_ACCESS_MODE_PUBLIC {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED, nil
	}
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || principal.Banned || principal.IdentityID == "" {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_AUTHENTICATION_REQUIRED, nil
	}
	active, err := identitystate.LockActivePrincipal(ctx, db, principal)
	if err != nil {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_UNSPECIFIED, err
	}
	if !active {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_AUTHENTICATION_REQUIRED, nil
	}
	if policy.Mode == commonv1.PageAccessMode_PAGE_ACCESS_MODE_AUTHENTICATED {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED, nil
	}
	if checker == nil {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_UNSPECIFIED, fmt.Errorf("Page access permission checker is required")
	}
	manage, err := policyv1.Page.Manage(pageID)
	if err != nil {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_UNSPECIFIED, err
	}
	allowed, err := check(ctx, checker, manage)
	if err != nil {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_UNSPECIFIED, err
	}
	if allowed {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED, nil
	}
	groups, err := conditionGroups(ctx, db, checker, principal, policy)
	if err != nil {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_UNSPECIFIED, err
	}
	if matchesGroups(groups, policy.Match) {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED, nil
	}
	return commonv1.PageAccessReason_PAGE_ACCESS_REASON_CONDITIONS_NOT_MET, nil
}

// conditionGroups reports, for each condition group the policy enables,
// whether principal meets it.
func conditionGroups(
	ctx context.Context,
	db *gorm.DB,
	checker PermissionChecker,
	principal *auth.UserInfo,
	policy *commonv1.PageAccessPolicy,
) ([]bool, error) {
	groups := make([]bool, 0, 3)
	if len(policy.AllowedRoles) > 0 {
		matches, err := holdsAnyRole(ctx, checker, policy.AllowedRoles)
		if err != nil {
			return nil, err
		}
		groups = append(groups, matches)
	}
	if len(policy.UserTagIds) > 0 {
		var matches bool
		if principal.MemberID != "" {
			if err := db.WithContext(ctx).Raw("SELECT EXISTS(SELECT 1 FROM user_tag_mapping WHERE member_id = ? AND tag_id IN ?)", principal.MemberID.String(), policy.UserTagIds).Scan(&matches).Error; err != nil {
				return nil, err
			}
		}
		groups = append(groups, matches)
	}
	if policy.NewsletterSubscriber {
		var matches bool
		if err := db.WithContext(ctx).Raw("SELECT EXISTS(SELECT 1 FROM newsletter_subscription WHERE identity_id = ?)", principal.IdentityID.String()).Scan(&matches).Error; err != nil {
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

// EvaluateStored reads the policy from the Page root held by its caller.
func EvaluateStored(ctx context.Context, db *gorm.DB, checker PermissionChecker, pageID string) (commonv1.PageAccessReason, error) {
	var row struct {
		AccessPolicy json.RawMessage `gorm:"column:access_policy"`
	}
	if err := db.WithContext(ctx).Table("page").Select("access_policy").Where("id = ?", pageID).Take(&row).Error; err != nil {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_UNSPECIFIED, err
	}
	return Evaluate(ctx, db, checker, pageID, row.AccessPolicy)
}

func matchesGroups(groups []bool, match commonv1.PageAccessMatch) bool {
	for _, value := range groups {
		if match == commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ALL && !value {
			return false
		}
		if match != commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ALL && value {
			return true
		}
	}
	return len(groups) > 0 && match == commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ALL
}

func check(ctx context.Context, checker PermissionChecker, can policyv1.Can) (bool, error) {
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, err
	}
	return checker.Can(ctx, decision)
}

// Require is used by authenticated Page and File reads sharing the same root.
func Require(ctx context.Context, db *gorm.DB, checker PermissionChecker, pageID string, raw json.RawMessage) error {
	reason, err := Evaluate(ctx, db, checker, pageID, raw)
	if err != nil {
		return failurecause.WithAttrs(err, slog.String("page_id", pageID))
	}
	switch reason {
	case commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED:
		return nil
	case commonv1.PageAccessReason_PAGE_ACCESS_REASON_AUTHENTICATION_REQUIRED:
		return domainerr.AuthenticationRequired()
	default:
		return domainerr.Denied("you do not have permission to access this page")
	}
}
