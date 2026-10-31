package label

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

var labelEntity = entityparticipant.Spec{
	Name: "label", Title: "Label", Article: "a Label",
	Table: "label", IDColumn: "label_id", OwnerTable: "label_owner", ManagerTable: "label_manager",
	ParentColumn: "parent_label_id", ParentGraphLock: "label_parent_graph",
	Policy: policyv1.Label,
}

// requireLabelView requires View. Unlike RequirePermission, it answers a
// caller without a principal not found.
func requireLabelView(ctx context.Context, checker entityparticipant.PermissionChecker, labelID string) error {
	return labelEntity.RequirePrincipalPermission(ctx, checker, labelID, policyv1.Label.View, auth.GetUser(ctx))
}

func labelAllowedActions(ctx context.Context, checker entityparticipant.PermissionChecker, labelID, status string) ([]managev1.LabelAction, error) {
	publishAction := managev1.LabelAction_LABEL_ACTION_PUBLISH
	if status == managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String() {
		publishAction = managev1.LabelAction_LABEL_ACTION_UNPUBLISH
	}
	return entityparticipant.AllowedActions(ctx, labelEntity, checker, labelID, []entityparticipant.ActionCheck[managev1.LabelAction]{
		{Can: policyv1.Label.Edit, Action: managev1.LabelAction_LABEL_ACTION_EDIT},
		{Can: policyv1.Label.ManageShareLinks, Action: managev1.LabelAction_LABEL_ACTION_MANAGE_SHARE_LINKS},
		{Can: policyv1.Label.Delete, Action: managev1.LabelAction_LABEL_ACTION_DELETE},
		{Can: policyv1.Label.ManageParticipants, Action: managev1.LabelAction_LABEL_ACTION_MANAGE_PARTICIPANTS},
		{Can: policyv1.Label.Publish, Action: publishAction},
		{Can: policyv1.Label.RemoveOwner, Action: managev1.LabelAction_LABEL_ACTION_REMOVE_OWNER},
	})
}

// errAuthenticationRequired answers a caller without an authenticated
// principal.
var errAuthenticationRequired = domainerr.New(domainerr.Unauthenticated, "", "authentication required")
