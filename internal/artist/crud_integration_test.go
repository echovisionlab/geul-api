//go:build integration

package artist_test

import (
	"context"
	artist "github.com/echovisionlab/geul-api/internal/artist"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// artistIntegrationHandler is the manage Connect adapter of an Artist
// service with the SpiceDB client and content store it runs on.
type artistIntegrationHandler struct {
	*artist.ConnectHandler
	spiceDB       *auth.SpiceDBClient
	contentBlocks *contentblock.Store
}

// newArtistIntegrationSpiceDBService returns the manage Connect adapter of
// an Artist service whose administrator is adminID.
func newArtistIntegrationSpiceDBService(t *testing.T, db *gorm.DB, adminID string) artistIntegrationHandler {
	t.Helper()
	stack := testutil.SetupOryStack(t)
	syncArtistIntegrationGlobalRole(t, stack.SpiceDBClient, adminID, policyv1.Role.Admin())
	contentBlocks := testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient)
	return artistIntegrationHandler{
		ConnectHandler: artist.NewConnectHandler(newTestService(t, integrationDeps{
			DB: db, SpiceDB: stack.SpiceDBClient, ContentBlocks: contentBlocks,
		})),
		spiceDB: stack.SpiceDBClient, contentBlocks: contentBlocks,
	}
}

func syncArtistIntegrationGlobalRole(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	identityID string,
	role policyv1.RoleID,
) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, role)
	require.NoError(t, err)
}

func artistIntegrationAdminCtx(id string) context.Context {
	return auth.WithUser(requestBackground(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(id),
		MemberID:      auth.MemberID(artistMemberID(id)),
		SessionID:     auth.SessionID(testutil.IntegrationUUID()),
		Authenticated: true,
		Onboarded:     true,
	})
}

func TestArtistImageGalleryCASAndFilePreservationIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	adminID := testutil.IntegrationUUID()
	seedArtistIdentity(t, db, adminID, "Artist Gallery Admin")
	service := newArtistIntegrationSpiceDBService(t, db, adminID)
	ctx := artistIntegrationAdminCtx(adminID)

	created, err := service.CreateArtist(ctx, connect.NewRequest(&managev1.CreateArtistRequest{
		Name:     "Artist Gallery",
		Document: artistContentDocument("en", "Artist Gallery"),
	}))
	require.NoError(t, err)
	firstFileID := testutil.IntegrationUUID()
	secondFileID := testutil.IntegrationUUID()
	seedArtistImageFile(t, db, firstFileID, "first")
	seedArtistImageFile(t, db, secondFileID, "second")

	editorData, err := service.GetArtistEditorData(ctx, connect.NewRequest(&managev1.GetArtistEditorDataRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Empty(t, editorData.Msg.Artist.Images)
	require.NotEmpty(t, editorData.Msg.ImageRevision)
	emptyRevision := editorData.Msg.ImageRevision

	set, err := service.SetArtistImages(ctx, connect.NewRequest(&managev1.SetArtistImagesRequest{
		ArtistId:         created.Msg.Id,
		FileIds:          []string{secondFileID, firstFileID},
		ExpectedRevision: emptyRevision,
	}))
	require.NoError(t, err)
	require.Len(t, set.Msg.Images, 2)
	require.Equal(t, secondFileID, set.Msg.Images[0].FileId)
	require.True(t, set.Msg.Images[0].Primary)
	require.Equal(t, firstFileID, set.Msg.Images[1].FileId)
	require.False(t, set.Msg.Images[1].Primary)
	require.NotEmpty(t, set.Msg.Images[0].GetAsset().GetUrl())

	_, err = service.SetArtistImages(ctx, connect.NewRequest(&managev1.SetArtistImagesRequest{
		ArtistId:         created.Msg.Id,
		FileIds:          []string{firstFileID, secondFileID},
		ExpectedRevision: emptyRevision,
	}))
	require.Error(t, err)

	reordered, err := service.SetArtistImages(ctx, connect.NewRequest(&managev1.SetArtistImagesRequest{
		ArtistId:         created.Msg.Id,
		FileIds:          []string{firstFileID, secondFileID},
		ExpectedRevision: set.Msg.Revision,
	}))
	require.NoError(t, err)
	require.Equal(t, firstFileID, reordered.Msg.Images[0].FileId)
	require.NotEqual(t, set.Msg.Revision, reordered.Msg.Revision)

	cleared, err := service.SetArtistImages(ctx, connect.NewRequest(&managev1.SetArtistImagesRequest{
		ArtistId:         created.Msg.Id,
		FileIds:          nil,
		ExpectedRevision: reordered.Msg.Revision,
	}))
	require.NoError(t, err)
	require.Empty(t, cleared.Msg.Images)
	var clearedBindingCount int64
	require.NoError(t, db.Model(&model.PublicAssetBinding{}).
		Where("owner_type = ? AND owner_id = ?", "artist", created.Msg.Id).
		Count(&clearedBindingCount).Error)
	require.Zero(t, clearedBindingCount)

	var fileCount int64
	require.NoError(t, db.Table("file").Where("id IN ?", []string{firstFileID, secondFileID}).Count(&fileCount).Error)
	require.Equal(t, int64(2), fileCount)

	thirdFileID := testutil.IntegrationUUID()
	seedArtistImageFile(t, db, thirdFileID, "third")
	setForDelete, err := service.SetArtistImages(ctx, connect.NewRequest(&managev1.SetArtistImagesRequest{
		ArtistId:         created.Msg.Id,
		FileIds:          []string{thirdFileID},
		ExpectedRevision: cleared.Msg.Revision,
	}))
	require.NoError(t, err)
	require.Len(t, setForDelete.Msg.Images, 1)
	var deleteBinding model.PublicAssetBinding
	require.NoError(t, db.Where(
		"owner_type = ? AND owner_id = ? AND binding_key = ?",
		"artist", created.Msg.Id, "image_"+thirdFileID,
	).Take(&deleteBinding).Error)
	preview, err := service.PreviewDeleteArtist(ctx, connect.NewRequest(&managev1.PreviewDeleteArtistRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	_, err = service.DeleteArtist(ctx, connect.NewRequest(&managev1.DeleteArtistRequest{
		Id: created.Msg.Id, ExpectedRevision: preview.Msg.Revision,
	}))
	require.NoError(t, err)
	var deleteBindingCount int64
	require.NoError(t, db.Model(&model.PublicAssetBinding{}).Where(
		"owner_type = ? AND owner_id = ? AND binding_key = ?",
		deleteBinding.OwnerType, deleteBinding.OwnerID, deleteBinding.BindingKey,
	).Count(&deleteBindingCount).Error)
	require.Zero(t, deleteBindingCount)
	var preservedAsset model.PublicAsset
	require.NoError(t, db.Select("id", "status").Take(&preservedAsset, "id = ?", deleteBinding.AssetID).Error)
	require.Equal(t, model.PublicAssetStatusReady, preservedAsset.Status)
	require.NoError(t, db.Table("file").Where("id = ?", thirdFileID).Count(&fileCount).Error)
	require.Equal(t, int64(1), fileCount)
}

func TestArtistOwnerManagerTransitionsIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	adminID := testutil.IntegrationUUID()
	participantID := testutil.IntegrationUUID()
	seedArtistIdentity(t, db, adminID, "Artist Owner Admin")
	seedArtistIdentity(t, db, participantID, "Artist Participant")
	service := newArtistIntegrationSpiceDBService(t, db, adminID)
	adminCtx := artistIntegrationAdminCtx(adminID)
	participantCtx := auth.WithUser(requestBackground(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(participantID),
		MemberID:      auth.MemberID(artistMemberID(participantID)),
		SessionID:     auth.SessionID(testutil.IntegrationUUID()),
		Authenticated: true,
		Onboarded:     true,
	})

	created, err := service.CreateArtist(adminCtx, connect.NewRequest(&managev1.CreateArtistRequest{
		Name:     "Artist Roles",
		Document: artistContentDocument("en", "Artist Roles"),
	}))
	require.NoError(t, err)
	participantMemberID := artistMemberID(participantID)
	_, err = service.SetArtistParticipant(adminCtx, connect.NewRequest(&managev1.SetArtistParticipantRequest{
		ArtistId: created.Msg.Id,
		MemberId: participantMemberID,
		Role:     managev1.ArtistParticipantRole_ARTIST_PARTICIPANT_ROLE_MANAGER,
	}))
	require.NoError(t, err)
	requireArtistCapabilities(t, service, participantID, created.Msg.Id, "manager", true,
		policyv1.Artist.View,
		policyv1.Artist.Edit,
		policyv1.Artist.Manage,
	)
	requireArtistCapabilities(t, service, participantID, created.Msg.Id, "manager", false,
		policyv1.Artist.Delete,
		policyv1.Artist.Publish,
		policyv1.Artist.ManageParticipants,
		policyv1.Artist.ManageShareLinks,
	)
	requireArtistCapabilities(t, service, adminID, created.Msg.Id, "admin", true,
		policyv1.Artist.View,
		policyv1.Artist.Edit,
		policyv1.Artist.Delete,
		policyv1.Artist.Publish,
		policyv1.Artist.Manage,
		policyv1.Artist.ManageParticipants,
		policyv1.Artist.ManageShareLinks,
	)

	_, err = service.SetArtistParticipant(adminCtx, connect.NewRequest(&managev1.SetArtistParticipantRequest{
		ArtistId: created.Msg.Id,
		MemberId: participantMemberID,
		Role:     managev1.ArtistParticipantRole_ARTIST_PARTICIPANT_ROLE_OWNER,
	}))
	require.NoError(t, err)
	requireArtistCapabilities(t, service, participantID, created.Msg.Id, "owner", true,
		policyv1.Artist.View,
		policyv1.Artist.Edit,
		policyv1.Artist.Delete,
		policyv1.Artist.Publish,
		policyv1.Artist.Manage,
		policyv1.Artist.ManageParticipants,
		policyv1.Artist.ManageShareLinks,
	)
	_, err = service.SetArtistParticipant(participantCtx, connect.NewRequest(&managev1.SetArtistParticipantRequest{
		ArtistId: created.Msg.Id,
		MemberId: participantMemberID,
		Role:     managev1.ArtistParticipantRole_ARTIST_PARTICIPANT_ROLE_MANAGER,
	}))
	require.Error(t, err)

	_, err = service.SetArtistParticipant(adminCtx, connect.NewRequest(&managev1.SetArtistParticipantRequest{
		ArtistId: created.Msg.Id,
		MemberId: participantMemberID,
		Role:     managev1.ArtistParticipantRole_ARTIST_PARTICIPANT_ROLE_MANAGER,
	}))
	require.NoError(t, err)
	_, err = service.RemoveArtistParticipant(adminCtx, connect.NewRequest(&managev1.RemoveArtistParticipantRequest{
		ArtistId: created.Msg.Id,
		MemberId: artistMemberID(adminID),
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	_, err = service.RemoveArtistParticipant(adminCtx, connect.NewRequest(&managev1.RemoveArtistParticipantRequest{
		ArtistId: created.Msg.Id,
		MemberId: participantMemberID,
	}))
	require.NoError(t, err)
}

func requireArtistCapabilities(
	t *testing.T,
	service artistIntegrationHandler, accountIdentityID string,
	artistID string,
	actorLabel string,
	want bool,
	capabilities ...func(string) (policyv1.Can, error),
) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(accountIdentityID)
	require.NoError(t, err)
	for _, capability := range capabilities {
		can, canErr := capability(artistID)
		require.NoError(t, canErr)
		allowed, checkErr := service.spiceDB.CheckActorCan(t.Context(), actor, can)
		require.NoError(t, checkErr)
		require.Equal(
			t,
			want,
			allowed,
			"%s capability %s must match the generated Artist permission expression",
			actorLabel,
			can.Action().Name(),
		)
	}
}
