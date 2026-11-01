package artist

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

var artistEntity = entityparticipant.Spec{
	Name: "artist", Title: "Artist", Article: "an Artist",
	IDColumn: "artist_id", ParentColumn: "parent_artist_id",
	Policy: policyv1.Artist,
}

func artistAllowedActions(ctx context.Context, checker entityparticipant.PermissionChecker, artistID string, status Status) ([]Action, error) {
	publishAction := ActionPublish
	if status == StatusPublished {
		publishAction = ActionUnpublish
	}
	return entityparticipant.AllowedActions(ctx, artistEntity, checker, artistID, []entityparticipant.ActionCheck[Action]{
		{Can: policyv1.Artist.Edit, Action: ActionEdit},
		{Can: policyv1.Artist.ManageShareLinks, Action: ActionManageShareLinks},
		{Can: policyv1.Artist.Delete, Action: ActionDelete},
		{Can: policyv1.Artist.ManageParticipants, Action: ActionManageParticipants},
		{Can: policyv1.Artist.Publish, Action: publishAction},
		{Can: policyv1.Artist.RemoveOwner, Action: ActionRemoveOwner},
	})
}

// errAuthenticationRequired answers a caller without an authenticated
// principal.
var errAuthenticationRequired = domainerr.New(domainerr.Unauthenticated, "", "authentication required")
