package release

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
)

// PolicyAccess decides an exact Track download policy consumer's Release
// authority in its own transaction: the Release root and the active
// principal are locked before one object permission is checked.
type PolicyAccess struct {
	checker *auth.SpiceDBClient
}

func NewPolicyAccess(checker *auth.SpiceDBClient) *PolicyAccess {
	return &PolicyAccess{checker: checker}
}

func (a *PolicyAccess) RequireLockedView(ctx context.Context, tx *gorm.DB, releaseID string) error {
	return connectError(releasedomain.RequireLockedView(ctx, releasepostgres.NewStore(tx, releasepostgres.Ports{}), lockActive(ctx, tx), a.checker, releaseID))
}

func (a *PolicyAccess) RequireLockedEdit(ctx context.Context, tx *gorm.DB, releaseID string) error {
	return connectError(releasedomain.RequireLockedEdit(ctx, releasepostgres.NewStore(tx, releasepostgres.Ports{}), lockActive(ctx, tx), a.checker, releaseID))
}

// lockActive locks a principal's Identity in tx and reports whether it is
// still active.
func lockActive(ctx context.Context, tx *gorm.DB) func(*auth.UserInfo) (bool, error) {
	return func(principal *auth.UserInfo) (bool, error) {
		return identitystate.LockActivePrincipal(ctx, tx, principal)
	}
}
