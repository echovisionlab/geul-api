package release

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	"gorm.io/gorm"
)

type PolicyAccess struct {
	checker   *auth.SpiceDBClient
	authority *releasedomain.PolicyAuthority
}

func NewPolicyAccess(checker *auth.SpiceDBClient) *PolicyAccess {
	return &PolicyAccess{checker: checker, authority: releasedomain.NewPolicyAuthority()}
}

func (a *PolicyAccess) RequireLockedView(ctx context.Context, tx *gorm.DB, releaseID string) error {
	return a.authority.RequireLockedView(ctx, tx, a.checker, releaseID)
}

func (a *PolicyAccess) RequireLockedEdit(ctx context.Context, tx *gorm.DB, releaseID string) error {
	return a.authority.RequireLockedEdit(ctx, tx, a.checker, releaseID)
}
