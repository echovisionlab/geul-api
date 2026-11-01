// Package identitystatetest provides the Identity fence unit tests use in
// place of identitystate.PostgresFence. It takes no lock and reads no Member
// or Kratos row: every principal with a Member and an Identity is active
// unless a test deactivates it. Lock order and the fence's SQL stay covered
// by the identitystate integration tests.
package identitystatetest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/echovisionlab/geul-api/internal/auth"
	"gorm.io/gorm"
)

// Fence implements identitystate.Fence. The zero value finds every principal
// active.
type Fence struct {
	// Err fails every Lock, LockActivePrincipal and WithMutation.
	Err error

	mu       sync.Mutex
	inactive map[string]bool
	locked   []string
}

// Deactivate makes LockActivePrincipal report identityID inactive, as a
// deactivated, deleted, banned or not yet onboarded principal is.
func (f *Fence) Deactivate(identityID string) *Fence {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inactive == nil {
		f.inactive = map[string]bool{}
	}
	f.inactive[identityID] = true
	return f
}

// Lock implements identitystate.Fence.
func (f *Fence) Lock(_ *gorm.DB, identityID string) error {
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return errors.New("user identity state fence requires user id")
	}
	return f.lock(identityID)
}

// LockActivePrincipal implements identitystate.Fence. Like the production
// fence, a principal without a Member or an Identity is not active.
func (f *Fence) LockActivePrincipal(_ context.Context, _ *gorm.DB, principal *auth.UserInfo) (bool, error) {
	if principal == nil || strings.TrimSpace(principal.MemberID.String()) == "" || strings.TrimSpace(principal.IdentityID.String()) == "" {
		return false, nil
	}
	identityID := principal.IdentityID.String()
	if err := f.lock(identityID); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.inactive[identityID], nil
}

// WithMutation implements identitystate.Fence by running work on db.
func (f *Fence) WithMutation(ctx context.Context, db *gorm.DB, identityID string, work func(context.Context, *gorm.DB) error) error {
	if db == nil || work == nil {
		return errors.New("user identity state mutation requires database and work")
	}
	if err := f.Lock(db, identityID); err != nil {
		return err
	}
	return work(ctx, db.WithContext(ctx))
}

func (f *Fence) lock(identityID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	f.locked = append(f.locked, identityID)
	return nil
}

// Locked returns the Identities fenced so far, in order.
func (f *Fence) Locked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.locked)
}
