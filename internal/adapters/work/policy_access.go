package work

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	"gorm.io/gorm"
)

type PolicyAccess struct {
	authority *workdomain.PolicyAuthority
}

func NewPolicyAccess(checker *auth.SpiceDBClient) *PolicyAccess {
	return &PolicyAccess{authority: workdomain.NewPolicyAuthority(checker)}
}

func (a *PolicyAccess) RequireLockedView(ctx context.Context, tx *gorm.DB, workID string) error {
	return a.authority.RequireLockedView(ctx, tx, workID)
}

func (a *PolicyAccess) RequireLockedEdit(ctx context.Context, tx *gorm.DB, workID string) error {
	return a.authority.RequireLockedEdit(ctx, tx, workID)
}
