package filemediapolicy

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"gorm.io/gorm"
)

type LockedPolicyAccess interface {
	RequireLockedView(context.Context, *gorm.DB, string) error
	RequireLockedEdit(context.Context, *gorm.DB, string) error
}
type EventPolicyAccess interface {
	RequireLockedView(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error
	RequireLockedEdit(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error
}
type PolicyOwnerPorts struct {
	Posts, Pages, Works, Releases LockedPolicyAccess
	Events                        EventPolicyAccess
	SpiceDB                       *auth.SpiceDBClient
}
type PolicyOwners struct{ ports PolicyOwnerPorts }

func NewPolicyOwners(ports PolicyOwnerPorts) *PolicyOwners { return &PolicyOwners{ports: ports} }
func (a *PolicyOwners) RequirePolicyView(ctx context.Context, tx *gorm.DB, kind, id string) error {
	return a.require(ctx, tx, kind, id, false)
}
func (a *PolicyOwners) RequirePolicyEdit(ctx context.Context, tx *gorm.DB, kind, id string) error {
	return a.require(ctx, tx, kind, id, true)
}
func (a *PolicyOwners) require(ctx context.Context, tx *gorm.DB, kind, id string, edit bool) error {
	if kind == "program_event" {
		if edit {
			return a.ports.Events.RequireLockedEdit(ctx, tx, a.ports.SpiceDB, id)
		}
		return a.ports.Events.RequireLockedView(ctx, tx, a.ports.SpiceDB, id)
	}
	var access LockedPolicyAccess
	switch kind {
	case "post":
		access = a.ports.Posts
	case "page":
		access = a.ports.Pages
	case "work":
		access = a.ports.Works
	case "release":
		access = a.ports.Releases
	default:
		return fmt.Errorf("unsupported File policy owner %s", kind)
	}
	if edit {
		return access.RequireLockedEdit(ctx, tx, id)
	}
	return access.RequireLockedView(ctx, tx, id)
}
