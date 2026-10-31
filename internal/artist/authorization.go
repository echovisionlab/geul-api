package artist

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

var artistEntity = entityparticipant.Spec{
	Name: "artist", Title: "Artist", Article: "an Artist",
	Table: "artist", IDColumn: "artist_id", OwnerTable: "artist_owner", ManagerTable: "artist_manager",
	ParentColumn: "parent_artist_id", ParentGraphLock: "artist_parent_graph",
	Policy: policyv1.Artist,
}

func artistAllowedActions(ctx context.Context, checker entityparticipant.PermissionChecker, artistID, status string) ([]managev1.ArtistAction, error) {
	publishAction := managev1.ArtistAction_ARTIST_ACTION_PUBLISH
	if status == managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String() {
		publishAction = managev1.ArtistAction_ARTIST_ACTION_UNPUBLISH
	}
	return entityparticipant.AllowedActions(ctx, artistEntity, checker, artistID, []entityparticipant.ActionCheck[managev1.ArtistAction]{
		{Can: policyv1.Artist.Edit, Action: managev1.ArtistAction_ARTIST_ACTION_EDIT},
		{Can: policyv1.Artist.ManageShareLinks, Action: managev1.ArtistAction_ARTIST_ACTION_MANAGE_SHARE_LINKS},
		{Can: policyv1.Artist.Delete, Action: managev1.ArtistAction_ARTIST_ACTION_DELETE},
		{Can: policyv1.Artist.ManageParticipants, Action: managev1.ArtistAction_ARTIST_ACTION_MANAGE_PARTICIPANTS},
		{Can: policyv1.Artist.Publish, Action: publishAction},
		{Can: policyv1.Artist.RemoveOwner, Action: managev1.ArtistAction_ARTIST_ACTION_REMOVE_OWNER},
	})
}

func hasArtistAction(actions []managev1.ArtistAction, expected managev1.ArtistAction) bool {
	for _, action := range actions {
		if action == expected {
			return true
		}
	}
	return false
}
