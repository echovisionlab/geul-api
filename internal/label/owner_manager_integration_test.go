//go:build integration

package label

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestLabelOwnerManagerAuthorityAndDeletionPreviewIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminIdentityID := integrationTestUUID()
	managerIdentityID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminIdentityID, "Label Owner")
	seedExternalKratosIdentityWithTraits(t, db, managerIdentityID, "Label Manager")

	adminMemberID := integrationMemberID(adminIdentityID)
	managerMemberID := integrationMemberID(managerIdentityID)
	adminCtx := artistIntegrationAdminCtx(adminIdentityID)
	managerCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(managerIdentityID),
		MemberID:      auth.MemberID(managerMemberID),
		SessionID:     auth.SessionID(integrationTestUUID()),
		Authenticated: true,
	})
	service := newLabelIntegrationService(t, db, adminIdentityID, &recordingArtistFileDeleter{})
	slug := "label-owner-manager-" + integrationTestUUID()
	created, err := service.CreateLabel(adminCtx, connect.NewRequest(&managev1.CreateLabelRequest{
		Name: "Label Owner Manager",
		Slug: &slug,
	}))
	require.NoError(t, err)
	require.NotNil(t, created.Msg.Document)
	require.NotEmpty(t, created.Msg.Revision)
	labelID := created.Msg.Id
	requireResourceManagerRow(t, db, "label_owner", "label_id", labelID, adminMemberID)

	_, err = service.SetLabelParticipant(adminCtx, connect.NewRequest(&managev1.SetLabelParticipantRequest{
		LabelId:  labelID,
		MemberId: managerMemberID,
		Role:     managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_MANAGER,
	}))
	require.NoError(t, err)
	requireResourceManagerRow(t, db, "label_manager", "label_id", labelID, managerMemberID)
	managerActor, err := policyv1.NewAccountIdentityActor(managerIdentityID)
	require.NoError(t, err)
	adminActor, err := policyv1.NewAccountIdentityActor(adminIdentityID)
	require.NoError(t, err)
	for _, action := range []func(string) (policyv1.Can, error){policyv1.Label.View, policyv1.Label.Edit, policyv1.Label.Manage} {
		can, canErr := action(labelID)
		require.NoError(t, canErr)
		allowed, checkErr := service.spiceDB.CheckActorCan(context.Background(), managerActor, can)
		require.NoError(t, checkErr)
		require.True(t, allowed, "manager must have %s through the generated Label permission expression", can.Action().Permission())
	}
	for _, action := range []func(string) (policyv1.Can, error){policyv1.Label.Delete, policyv1.Label.Publish, policyv1.Label.ManageParticipants, policyv1.Label.ManageShareLinks} {
		can, canErr := action(labelID)
		require.NoError(t, canErr)
		allowed, checkErr := service.spiceDB.CheckActorCan(context.Background(), managerActor, can)
		require.NoError(t, checkErr)
		require.False(t, allowed, "manager must not have %s through the generated Label permission expression", can.Action().Permission())
	}
	for _, action := range []func(string) (policyv1.Can, error){policyv1.Label.View, policyv1.Label.Edit, policyv1.Label.Delete, policyv1.Label.Publish, policyv1.Label.Manage, policyv1.Label.ManageParticipants, policyv1.Label.ManageShareLinks} {
		can, canErr := action(labelID)
		require.NoError(t, canErr)
		allowed, checkErr := service.spiceDB.CheckActorCan(context.Background(), adminActor, can)
		require.NoError(t, checkErr)
		require.True(t, allowed, "admin must have %s through the generated Label permission expression", can.Action().Permission())
	}

	managerEditor, err := service.GetLabelEditorData(managerCtx, connect.NewRequest(&managev1.GetLabelEditorDataRequest{Id: labelID}))
	require.NoError(t, err)
	require.ElementsMatch(t, []managev1.LabelAction{managev1.LabelAction_LABEL_ACTION_EDIT}, managerEditor.Msg.AllowedActions)

	_, err = service.SetLabelParticipant(adminCtx, connect.NewRequest(&managev1.SetLabelParticipantRequest{
		LabelId: labelID, MemberId: managerMemberID,
		Role: managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_OWNER,
	}))
	require.NoError(t, err)
	for _, action := range []func(string) (policyv1.Can, error){policyv1.Label.View, policyv1.Label.Edit, policyv1.Label.Delete, policyv1.Label.Publish, policyv1.Label.Manage, policyv1.Label.ManageParticipants, policyv1.Label.ManageShareLinks} {
		can, canErr := action(labelID)
		require.NoError(t, canErr)
		allowed, checkErr := service.spiceDB.CheckActorCan(context.Background(), managerActor, can)
		require.NoError(t, checkErr)
		require.True(t, allowed, "owner must have %s through the generated Label permission expression", can.Action().Permission())
	}
	_, err = service.SetLabelParticipant(adminCtx, connect.NewRequest(&managev1.SetLabelParticipantRequest{
		LabelId: labelID, MemberId: managerMemberID,
		Role: managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_MANAGER,
	}))
	require.NoError(t, err)

	_, err = service.PreviewDeleteLabel(managerCtx, connect.NewRequest(&managev1.PreviewDeleteLabelRequest{Id: labelID}))
	require.Error(t, err)

	participants, err := service.ListLabelParticipants(adminCtx, connect.NewRequest(&managev1.ListLabelParticipantsRequest{LabelId: labelID}))
	require.NoError(t, err)
	require.Len(t, participants.Msg.Participants, 2)

	_, err = service.RemoveLabelParticipant(adminCtx, connect.NewRequest(&managev1.RemoveLabelParticipantRequest{
		LabelId:  labelID,
		MemberId: adminMemberID,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	preview, err := service.PreviewDeleteLabel(adminCtx, connect.NewRequest(&managev1.PreviewDeleteLabelRequest{Id: labelID}))
	require.NoError(t, err)
	require.NotEmpty(t, preview.Msg.Revision)
	require.Empty(t, preview.Msg.Impacts)

	_, err = service.DeleteLabel(adminCtx, connect.NewRequest(&managev1.DeleteLabelRequest{
		Id:              labelID,
		PreviewRevision: "stale",
	}))
	require.Error(t, err)

	deleted, err := service.DeleteLabel(adminCtx, connect.NewRequest(&managev1.DeleteLabelRequest{
		Id:              labelID,
		PreviewRevision: preview.Msg.Revision,
	}))
	require.NoError(t, err)
	require.True(t, deleted.Msg.Success)
}
