package legal

import (
	"context"
)

// A deleted active/scheduled policy no longer owns the stable public OG route.
// Select another current policy if present, otherwise release the route.
func refreshLegalRouteAfterPolicyDeletion(ctx context.Context, store Store, kind *policyKind, id string, before *CurrentRoute) error {
	if before == nil || before.ID != id {
		return nil
	}
	current, err := store.CurrentRoute(ctx, kind.kind)
	if err != nil {
		return err
	}
	if current == nil {
		return store.ReleaseRoute(ctx, kind.kind)
	}
	return store.RequestOG(ctx, kind.kind, current.ID, "", true, kind.entity()+"_policy_deleted")
}
