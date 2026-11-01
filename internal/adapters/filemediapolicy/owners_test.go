package filemediapolicy

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type policyCall struct {
	kind, id string
	edit     bool
	tx       *gorm.DB
	ctx      context.Context
	spice    *auth.SpiceDBClient
}
type policyTap struct {
	kind  string
	calls *[]policyCall
}

func (p policyTap) RequireLockedView(ctx context.Context, tx *gorm.DB, id string) error {
	*p.calls = append(*p.calls, policyCall{kind: p.kind, id: id, tx: tx, ctx: ctx})
	return nil
}
func (p policyTap) RequireLockedEdit(ctx context.Context, tx *gorm.DB, id string) error {
	*p.calls = append(*p.calls, policyCall{kind: p.kind, id: id, edit: true, tx: tx, ctx: ctx})
	return nil
}

type eventTap struct{ calls *[]policyCall }

func (p eventTap) RequireLockedView(ctx context.Context, tx *gorm.DB, spice *auth.SpiceDBClient, id string) error {
	*p.calls = append(*p.calls, policyCall{kind: "program_event", id: id, tx: tx, ctx: ctx, spice: spice})
	return nil
}
func (p eventTap) RequireLockedEdit(ctx context.Context, tx *gorm.DB, spice *auth.SpiceDBClient, id string) error {
	*p.calls = append(*p.calls, policyCall{kind: "program_event", id: id, edit: true, tx: tx, ctx: ctx, spice: spice})
	return nil
}
func TestPolicyOwnersDelegateExactActionInOwningTransaction(t *testing.T) {
	for _, kind := range []string{"post", "page", "work", "release", "program_event"} {
		for _, edit := range []bool{false, true} {
			calls := []policyCall{}
			spice := &auth.SpiceDBClient{}
			owner := NewPolicyOwners(PolicyOwnerPorts{Posts: policyTap{"post", &calls}, Pages: policyTap{"page", &calls}, Works: policyTap{"work", &calls}, Releases: policyTap{"release", &calls}, Events: eventTap{&calls}, SpiceDB: spice})
			tx := &gorm.DB{}
			ctx := t.Context()
			var err error
			if edit {
				err = owner.RequirePolicyEdit(ctx, tx, kind, "owner")
			} else {
				err = owner.RequirePolicyView(ctx, tx, kind, "owner")
			}
			require.NoError(t, err)
			require.Len(t, calls, 1)
			require.Equal(t, kind, calls[0].kind)
			require.Equal(t, "owner", calls[0].id)
			require.Equal(t, edit, calls[0].edit)
			require.Same(t, tx, calls[0].tx)
			require.Equal(t, ctx, calls[0].ctx)
			if kind == "program_event" {
				require.Same(t, spice, calls[0].spice)
			}
		}
	}
}
