//go:build integration

package artist

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func newArtistIntegrationSpiceDBService(
	t *testing.T,
	db *gorm.DB,
	adminID string,
	fileDeleter *recordingArtistFileDeleter,
) *ArtistService {
	t.Helper()
	stack := testutil.SetupOryStack(t)
	syncArtistIntegrationGlobalRole(t, stack.SpiceDBClient, adminID, policyv1.Role.Admin())
	return NewArtistService(
		db,
		stack.SpiceDBClient,
		artistIdentityManager{identity: artistIdentity(adminID)},
		fileDeleter,
		noopArtistAsyncPublisher{},
		Dependencies{
			Translation: artistIntegrationTranslation{},
			Members:     artistIntegrationMemberProjection{db: db},
			Runtime:     newArtistIntegrationRuntime(db, ""),
		},
		WithArtistContentBlockStore(testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient)),
	)
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
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(id),
		MemberID:      auth.MemberID(artistMemberID(id)),
		SessionID:     auth.SessionID(testutil.IntegrationUUID()),
		Authenticated: true,
	})
}

func TestArtistCreateRechecksCurrentSiteAdminInTransactionIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	adminID := testutil.IntegrationUUID()
	seedArtistIdentity(t, db, adminID, "Artist Demoted Admin")
	service := newArtistIntegrationSpiceDBService(t, db, adminID, &recordingArtistFileDeleter{})

	syncArtistIntegrationGlobalRole(t, service.spiceDB, adminID, policyv1.Role.User())

	_, err := service.CreateArtist(
		artistIntegrationAdminCtx(adminID),
		connect.NewRequest(&managev1.CreateArtistRequest{Name: "Must Not Exist"}),
	)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	var count int64
	require.NoError(t, db.Table("artist").Count(&count).Error)
	require.Zero(t, count)
}

func TestListMyArtistsGivesCurrentSiteAdminAllArtistsIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	adminID := testutil.IntegrationUUID()
	otherAdminID := testutil.IntegrationUUID()
	seedArtistIdentity(t, db, adminID, "Artist Listing Admin")
	seedArtistIdentity(t, db, otherAdminID, "Other Artist Admin")
	service := newArtistIntegrationSpiceDBService(t, db, adminID, &recordingArtistFileDeleter{})
	syncArtistIntegrationGlobalRole(t, service.spiceDB, otherAdminID, policyv1.Role.Admin())

	created, err := service.CreateArtist(
		artistIntegrationAdminCtx(otherAdminID),
		connect.NewRequest(&managev1.CreateArtistRequest{
			Name: "Other Admin Artist",
		}),
	)
	require.NoError(t, err)
	require.NotNil(t, created.Msg.Document)
	require.NotEmpty(t, created.Msg.Revision)

	listed, err := service.ListMyArtists(
		artistIntegrationAdminCtx(adminID),
		connect.NewRequest(&managev1.ListMyArtistsRequest{}),
	)
	require.NoError(t, err)
	requireArtistIDs(t, listed.Msg.Artists, created.Msg.Id)
}

func requireResourceManagerRow(
	t *testing.T,
	db *gorm.DB,
	tableName string,
	resourceIDColumn string,
	resourceID string,
	memberID string,
) {
	t.Helper()

	var count int64
	result := db.Raw(
		`SELECT COUNT(*) FROM `+tableName+` WHERE `+resourceIDColumn+` = ? AND member_id = ?`,
		resourceID,
		memberID,
	).Scan(&count)
	require.NoError(t, result.Error)
	require.Equal(t, int64(1), count)
}

func requireArtistIDs(t *testing.T, artists []*managev1.Artist, expectedIDs ...string) {
	t.Helper()
	for _, expectedID := range expectedIDs {
		found := false
		for _, artist := range artists {
			if artist.GetId() == expectedID {
				found = true
				break
			}
		}
		require.Truef(t, found, "expected Artist %s in response", expectedID)
	}
}

func TestArtistImageGalleryCASAndFilePreservationIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	adminID := testutil.IntegrationUUID()
	seedArtistIdentity(t, db, adminID, "Artist Gallery Admin")
	service := newArtistIntegrationSpiceDBService(t, db, adminID, &recordingArtistFileDeleter{})
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
		"artist", created.Msg.Id, "image:"+thirdFileID,
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

func TestArtistDeletePreviewRejectsChangedRelationsAndPreservesTargetsIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	adminID := testutil.IntegrationUUID()
	seedArtistIdentity(t, db, adminID, "Artist Delete Admin")
	service := newArtistIntegrationSpiceDBService(t, db, adminID, &recordingArtistFileDeleter{})
	ctx := artistIntegrationAdminCtx(adminID)

	parent, err := service.CreateArtist(ctx, connect.NewRequest(&managev1.CreateArtistRequest{
		Name:     "Delete Parent",
		Document: artistContentDocument("en", "Delete Parent"),
	}))
	require.NoError(t, err)
	child, err := service.CreateArtist(ctx, connect.NewRequest(&managev1.CreateArtistRequest{
		Name:           "Delete Child",
		ParentArtistId: &parent.Msg.Id,
		Document:       artistContentDocument("en", "Delete Child"),
	}))
	require.NoError(t, err)
	labelID := testutil.IntegrationUUID()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		document, createErr := service.contentBlocks.CreateDocument(t.Context(), tx, contentblock.CreateInput{
			Profile: "compact", SourceLocale: "en",
		})
		if createErr != nil {
			return createErr
		}
		return tx.Exec(`
			INSERT INTO label (id, status, content_document_id, created_at, updated_at)
			VALUES (?::uuid, 'LABEL_STATUS_DRAFT', ?::uuid, now(), now())
		`, labelID, document.Document.ID).Error
	}))
	require.NoError(t, db.Exec(`INSERT INTO artist_label (artist_id, label_id, sort_order) VALUES (?::uuid, ?::uuid, 0)`, parent.Msg.Id, labelID).Error)

	preview, err := service.PreviewDeleteArtist(ctx, connect.NewRequest(&managev1.PreviewDeleteArtistRequest{Id: parent.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, int32(2), preview.Msg.TotalRelationCount)
	require.Len(t, preview.Msg.Impacts, 2)

	secondChild, err := service.CreateArtist(ctx, connect.NewRequest(&managev1.CreateArtistRequest{
		Name:           "Delete Child Two",
		ParentArtistId: &parent.Msg.Id,
		Document:       artistContentDocument("en", "Delete Child Two"),
	}))
	require.NoError(t, err)
	_, err = service.DeleteArtist(ctx, connect.NewRequest(&managev1.DeleteArtistRequest{
		Id:               parent.Msg.Id,
		ExpectedRevision: preview.Msg.Revision,
	}))
	require.Error(t, err)

	preview, err = service.PreviewDeleteArtist(ctx, connect.NewRequest(&managev1.PreviewDeleteArtistRequest{Id: parent.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, int32(3), preview.Msg.TotalRelationCount)
	deleted, err := service.DeleteArtist(ctx, connect.NewRequest(&managev1.DeleteArtistRequest{
		Id:               parent.Msg.Id,
		ExpectedRevision: preview.Msg.Revision,
	}))
	require.NoError(t, err)
	require.True(t, deleted.Msg.Success)

	var preservedLabelCount int64
	require.NoError(t, db.Table("label").Where("id = ?", labelID).Count(&preservedLabelCount).Error)
	require.Equal(t, int64(1), preservedLabelCount)
	for _, childID := range []string{child.Msg.Id, secondChild.Msg.Id} {
		var parentID *string
		require.NoError(t, db.Table("artist").Select("parent_artist_id").Where("id = ?", childID).Scan(&parentID).Error)
		require.Nil(t, parentID)
	}
}

func TestArtistDeleteSerializesRelationshipSnapshotWithReparentIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	adminID := testutil.IntegrationUUID()
	seedArtistIdentity(t, db, adminID, "Artist delete lock admin")
	service := newArtistIntegrationSpiceDBService(t, db, adminID, &recordingArtistFileDeleter{})
	ctx := artistIntegrationAdminCtx(adminID)

	parent, err := service.CreateArtist(ctx, connect.NewRequest(&managev1.CreateArtistRequest{
		Name:     "Delete lock parent",
		Document: artistContentDocument("en", "Delete lock parent"),
	}))
	require.NoError(t, err)
	preview, err := service.PreviewDeleteArtist(ctx, connect.NewRequest(&managev1.PreviewDeleteArtistRequest{Id: parent.Msg.Id}))
	require.NoError(t, err)

	graphBlocker := db.WithContext(ctx).Begin()
	require.NoError(t, graphBlocker.Error)
	t.Cleanup(func() { _ = graphBlocker.Rollback().Error })
	require.NoError(t, lockArtistParentGraph(ctx, graphBlocker))

	deleteDone := make(chan error, 1)
	go func() {
		_, deleteErr := service.DeleteArtist(ctx, connect.NewRequest(&managev1.DeleteArtistRequest{
			Id:               parent.Msg.Id,
			ExpectedRevision: preview.Msg.Revision,
		}))
		deleteDone <- deleteErr
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case deleteErr := <-deleteDone:
			require.FailNow(t, "Artist delete completed before the parent-graph lock was released", deleteErr)
		default:
		}

		probe := db.WithContext(ctx).Begin()
		require.NoError(t, probe.Error)
		var lockedArtistID string
		probeErr := probe.Raw(`
			SELECT id::text
			FROM artist
			WHERE id = ?::uuid
			FOR UPDATE NOWAIT
		`, parent.Msg.Id).Scan(&lockedArtistID).Error
		require.NoError(t, probe.Rollback().Error)
		var postgresErr *pgconn.PgError
		if errors.As(probeErr, &postgresErr) && postgresErr.Code == "55P03" {
			break
		}
		require.NoError(t, probeErr)
		if time.Now().After(deadline) {
			require.FailNow(t, "Artist delete did not reach the root-lock then parent-graph-lock boundary")
		}
		time.Sleep(10 * time.Millisecond)
	}

	require.NoError(t, graphBlocker.Commit().Error)
	select {
	case deleteErr := <-deleteDone:
		require.NoError(t, deleteErr)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "Artist delete did not resume after the parent-graph lock was released")
	}
}

func TestArtistOwnerManagerTransitionsIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	adminID := testutil.IntegrationUUID()
	participantID := testutil.IntegrationUUID()
	seedArtistIdentity(t, db, adminID, "Artist Owner Admin")
	seedArtistIdentity(t, db, participantID, "Artist Participant")
	service := newArtistIntegrationSpiceDBService(t, db, adminID, &recordingArtistFileDeleter{})
	adminCtx := artistIntegrationAdminCtx(adminID)
	participantCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(participantID),
		MemberID:      auth.MemberID(artistMemberID(participantID)),
		SessionID:     auth.SessionID(testutil.IntegrationUUID()),
		Authenticated: true,
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
	service *ArtistService,
	accountIdentityID string,
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
