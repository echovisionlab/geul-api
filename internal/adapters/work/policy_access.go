package work

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	workpostgres "github.com/echovisionlab/geul-api/internal/work/postgres"
	"gorm.io/gorm"
)

// PolicyAccess exposes the Work-owned lifecycle and active-principal fence
// to relation policy consumers, in their transaction.
type PolicyAccess struct {
	checker *auth.SpiceDBClient
}

func NewPolicyAccess(checker *auth.SpiceDBClient) *PolicyAccess {
	return &PolicyAccess{checker: checker}
}

func (a *PolicyAccess) RequireLockedView(ctx context.Context, tx *gorm.DB, workID string) error {
	return workdomain.RequireLockedView(ctx, workpostgres.Bind(tx, workpostgres.Ports{}, nil), identitystate.PostgresFence{}, a.checker, workID)
}

func (a *PolicyAccess) RequireLockedEdit(ctx context.Context, tx *gorm.DB, workID string) error {
	return workdomain.RequireLockedEdit(ctx, workpostgres.Bind(tx, workpostgres.Ports{}, nil), identitystate.PostgresFence{}, a.checker, workID)
}
