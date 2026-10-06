package pageaccess

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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
	groups := make([]bool, 0, 3)
	if len(policy.AllowedRoles) > 0 {
		matches := false
		for _, role := range policy.AllowedRoles {
			var can policyv1.Can
			switch role {
			case policyv1.AuthorizationRole_AUTHOR:
				can, err = policyv1.Platform.IsAuthor()
			case policyv1.AuthorizationRole_ADMIN:
				can, err = policyv1.Platform.IsAdmin()
			}
			if err != nil {
				return commonv1.PageAccessReason_PAGE_ACCESS_REASON_UNSPECIFIED, err
			}
			matches, err = check(ctx, checker, can)
			if err != nil {
				return commonv1.PageAccessReason_PAGE_ACCESS_REASON_UNSPECIFIED, err
			}
			if matches {
				break
			}
		}
		groups = append(groups, matches)
	}
	if len(policy.UserTagIds) > 0 {
		var matches bool
		if principal.MemberID != "" {
			err = db.WithContext(ctx).Raw("SELECT EXISTS(SELECT 1 FROM user_tag_mapping WHERE member_id = ? AND tag_id IN ?)", principal.MemberID.String(), policy.UserTagIds).Scan(&matches).Error
			if err != nil {
				return commonv1.PageAccessReason_PAGE_ACCESS_REASON_UNSPECIFIED, err
			}
		}
		groups = append(groups, matches)
	}
	if policy.NewsletterSubscriber {
		var matches bool
		if err = db.WithContext(ctx).Raw("SELECT EXISTS(SELECT 1 FROM newsletter_subscription WHERE identity_id = ?)", principal.IdentityID.String()).Scan(&matches).Error; err != nil {
			return commonv1.PageAccessReason_PAGE_ACCESS_REASON_UNSPECIFIED, err
		}
		groups = append(groups, matches)
	}
	if matchesGroups(groups, policy.Match) {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED, nil
	}
	return commonv1.PageAccessReason_PAGE_ACCESS_REASON_CONDITIONS_NOT_MET, nil
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
		return errs.Internal(err)
	}
	switch reason {
	case commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED:
		return nil
	case commonv1.PageAccessReason_PAGE_ACCESS_REASON_AUTHENTICATION_REQUIRED:
		return errs.AuthenticationRequired()
	default:
		return errs.PermissionDenied("you do not have permission to access this page")
	}
}
