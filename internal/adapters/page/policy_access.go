package page

import (
	"context"

	"gorm.io/gorm"
)

type policyAuthority interface {
	RequireLockedView(context.Context, *gorm.DB, string) error
	RequireLockedEdit(context.Context, *gorm.DB, string) error
}

// PolicyAccess delegates relation policy authorization to the Page owner.
type PolicyAccess struct {
	authority policyAuthority
}

func NewPolicyAccess(authority policyAuthority) *PolicyAccess {
	if authority == nil {
		panic("Page policy authority is required")
	}
	return &PolicyAccess{authority: authority}
}

func (a *PolicyAccess) RequireLockedView(ctx context.Context, tx *gorm.DB, pageID string) error {
	return a.authority.RequireLockedView(ctx, tx, pageID)
}

func (a *PolicyAccess) RequireLockedEdit(ctx context.Context, tx *gorm.DB, pageID string) error {
	return a.authority.RequireLockedEdit(ctx, tx, pageID)
}
