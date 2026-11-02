// Package pageaccesspostgres is the PostgreSQL pageaccess.Audience: the Member user
// tags and newsletter subscriptions a Page audience reads, and the identity
// fence, bound to one gorm transaction.
package pageaccesspostgres

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
)

// NewAudience binds the audience reads to tx, locking principals through
// fence.
func NewAudience(tx *gorm.DB, fence identitystate.Fence) pageaccess.Audience {
	return audience{tx: tx, fence: fence}
}

type audience struct {
	tx    *gorm.DB
	fence identitystate.Fence
}

func (a audience) LockActivePrincipal(ctx context.Context, principal *auth.UserInfo) (bool, error) {
	return a.fence.LockActivePrincipal(ctx, a.tx, principal)
}

func (a audience) HasUserTag(ctx context.Context, memberID string, tagIDs []string) (bool, error) {
	var matches bool
	err := a.tx.WithContext(ctx).Raw("SELECT EXISTS(SELECT 1 FROM user_tag_mapping WHERE member_id = ? AND tag_id IN ?)", memberID, tagIDs).Scan(&matches).Error
	return matches, err
}

func (a audience) SubscribesToNewsletter(ctx context.Context, identityID string) (bool, error) {
	var matches bool
	err := a.tx.WithContext(ctx).Raw("SELECT EXISTS(SELECT 1 FROM newsletter_subscription WHERE identity_id = ?)", identityID).Scan(&matches).Error
	return matches, err
}

// EvaluateStored reads the policy from the Page root held by its caller, for
// a File read that shares the Page's audience.
func EvaluateStored(ctx context.Context, tx *gorm.DB, checker pageaccess.PermissionChecker, pageID string) (pageaccess.Reason, error) {
	var row struct {
		AccessPolicy json.RawMessage `gorm:"column:access_policy"`
	}
	if err := tx.WithContext(ctx).Table("page").Select("access_policy").Where("id = ?", pageID).Take(&row).Error; err != nil {
		return pageaccess.ReasonUnspecified, err
	}
	return pageaccess.Evaluate(ctx, NewAudience(tx, identitystate.PostgresFence{}), checker, pageID, row.AccessPolicy)
}
