package documentsearchpostgres

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// CallerRoles answers documentsearch.CallerRoles from the current Member and Identity
// rows and the current platform role.
type CallerRoles struct {
	db      *gorm.DB
	fence   identitystate.Fence
	checker authz.AuthorizationDecisionChecker
}

func NewCallerRoles(db *gorm.DB, fence identitystate.Fence, checker authz.AuthorizationDecisionChecker) (*CallerRoles, error) {
	switch {
	case db == nil:
		return nil, errors.New("document search caller roles need a database")
	case fence == nil:
		return nil, errors.New("document search caller roles need the identity fence")
	case checker == nil:
		return nil, errors.New("document search caller roles need an authorization checker")
	}
	return &CallerRoles{db: db, fence: fence, checker: checker}, nil
}

// IsActive reports whether the caller is signed in with an active Member and Identity.
func (roles *CallerRoles) IsActive(ctx context.Context) (bool, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return false, nil
	}
	var active bool
	err := mutation.ReadCurrent(ctx, roles.db, func(tx *gorm.DB) error {
		var err error
		active, err = roles.fence.LockActivePrincipal(ctx, tx, principal)
		return err
	})
	return active, err
}

// IsAdmin reports the caller's Site Admin role.
func (roles *CallerRoles) IsAdmin(ctx context.Context) (bool, error) {
	return roles.can(ctx, policyv1.Platform.IsAdmin)
}

// IsAuthor reports the caller's Author role, which a Site Admin also holds.
func (roles *CallerRoles) IsAuthor(ctx context.Context) (bool, error) {
	return roles.can(ctx, policyv1.Platform.IsAuthor)
}

func (roles *CallerRoles) can(ctx context.Context, action func() (policyv1.Can, error)) (bool, error) {
	can, err := action()
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, err
	}
	return roles.checker.Can(ctx, decision)
}
