package legal

import (
	"context"

	"gorm.io/gorm"
)

// A deleted active/scheduled policy no longer owns the stable public OG route.
// Select another current policy if present, otherwise release the route.
func refreshLegalRouteAfterPolicyDeletion(ctx context.Context, tx *gorm.DB, og OG, kind, id string, before *CurrentRoute) error {
	if before == nil || before.ID != id {
		return nil
	}
	current, err := og.CurrentForRoute(ctx, tx, kind)
	if err != nil {
		return err
	}
	if current == nil {
		return og.CancelAndRelease(ctx, tx, kind, og.RouteID(kind))
	}
	return og.RequestSaved(ctx, tx, kind, current.ID, "", true, kind+"_policy_deleted")
}
