package label

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

var labelEntity = entityparticipant.Spec{
	Name: "label", Title: "Label", Article: "a Label",
	IDColumn: "label_id", ParentColumn: "parent_label_id",
	Policy: policyv1.Label,
}

// requireLabelView requires View. Unlike RequirePermission, it answers a
// caller without a principal not found.
func requireLabelView(ctx context.Context, checker entityparticipant.PermissionChecker, labelID string) error {
	return labelEntity.RequirePrincipalPermission(ctx, checker, labelID, policyv1.Label.View, auth.GetUser(ctx))
}

func labelAllowedActions(ctx context.Context, checker entityparticipant.PermissionChecker, labelID string, status Status) ([]Action, error) {
	publishAction := ActionPublish
	if status == StatusPublished {
		publishAction = ActionUnpublish
	}
	return entityparticipant.AllowedActions(ctx, labelEntity, checker, labelID, []entityparticipant.ActionCheck[Action]{
		{Can: policyv1.Label.Edit, Action: ActionEdit},
		{Can: policyv1.Label.ManageShareLinks, Action: ActionManageShareLinks},
		{Can: policyv1.Label.Delete, Action: ActionDelete},
		{Can: policyv1.Label.ManageParticipants, Action: ActionManageParticipants},
		{Can: policyv1.Label.Publish, Action: publishAction},
		{Can: policyv1.Label.RemoveOwner, Action: ActionRemoveOwner},
	})
}

// errAuthenticationRequired answers a caller without an authenticated
// principal.
var errAuthenticationRequired = domainerr.New(domainerr.Unauthenticated, "", "authentication required")
