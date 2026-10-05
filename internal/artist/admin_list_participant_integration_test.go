//go:build integration

package artist

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestArtistServiceListSlugAndManagerWorkflowsIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	adminID := testutil.IntegrationUUID()
	managerID := testutil.IntegrationUUID()
	adminMemberID := seedArtistIdentity(t, db, adminID, "Artist Workflow Admin")
	managerMemberID := seedArtistIdentity(t, db, managerID, "Artist Workflow Manager")
	artistSvc := newArtistIntegrationSpiceDBService(t, db, adminID, &recordingArtistFileDeleter{})

	adminCtx := artistIntegrationAdminCtx(adminID)
	publishedSlug := "artist-list-published-" + testutil.IntegrationUUID()
	draftSlug := "artist-list-draft-" + testutil.IntegrationUUID()

	published, err := artistSvc.CreateArtist(adminCtx, connect.NewRequest(&managev1.CreateArtistRequest{
		Name:        "Artist List Published",
		Slug:        &publishedSlug,
		CountryCode: artistStringPointer("KR"),
		Document:    artistContentDocument("en", "Artist List Published"),
	}))
	require.NoError(t, err)
	_, err = artistSvc.PublishArtist(adminCtx, connect.NewRequest(&managev1.PublishArtistRequest{Id: published.Msg.Id}))
	require.NoError(t, err)

	draft, err := artistSvc.CreateArtist(adminCtx, connect.NewRequest(&managev1.CreateArtistRequest{
		Name:        "Artist List Draft",
		Slug:        &draftSlug,
		CountryCode: artistStringPointer("US"),
		Document:    artistContentDocument("en", "Artist List Draft"),
	}))
	require.NoError(t, err)

	publicList, err := artistSvc.ListArtists(context.Background(), connect.NewRequest(&managev1.ListArtistsRequest{
		Filters: []*commonv1.FilterSpec{{
			Field: "search",
			Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
			Value: "Artist List",
		}},
	}))
	require.NoError(t, err)
	requireArtistIDs(t, publicList.Msg.Artists, published.Msg.Id)
	requireNotArtistIDs(t, publicList.Msg.Artists, draft.Msg.Id)

	draftFilterList, err := artistSvc.ListArtists(context.Background(), connect.NewRequest(&managev1.ListArtistsRequest{
		Filters: []*commonv1.FilterSpec{{
			Field: "status",
			Op:    commonv1.FilterOp_FILTER_OP_EQ,
			Value: managev1.ArtistStatus_ARTIST_STATUS_DRAFT.String(),
		}},
	}))
	require.NoError(t, err)
	requireNotArtistIDs(t, draftFilterList.Msg.Artists, draft.Msg.Id)

	adminDrafts, err := artistSvc.ListArtistsAdmin(adminCtx, connect.NewRequest(&managev1.ListArtistsAdminRequest{
		Filters: []*commonv1.FilterSpec{{
			Field: "status",
			Op:    commonv1.FilterOp_FILTER_OP_EQ,
			Value: managev1.ArtistStatus_ARTIST_STATUS_DRAFT.String(),
		}, {
			Field: "search",
			Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
			Value: "Artist List Draft",
		}},
		Pagination: &commonv1.PaginationRequest{Limit: 1},
	}))
	require.NoError(t, err)
	require.Equal(t, int32(1), adminDrafts.Msg.Pagination.Total)
	require.Len(t, adminDrafts.Msg.Artists, 1)
	require.Equal(t, draft.Msg.Id, adminDrafts.Msg.Artists[0].Artist.Id)

	unavailable, err := artistSvc.CheckArtistSlugAvailable(adminCtx, connect.NewRequest(&managev1.CheckArtistSlugAvailableRequest{
		Slug: publishedSlug,
	}))
	require.NoError(t, err)
	require.False(t, unavailable.Msg.Available)

	availableWhenExcludingSelf, err := artistSvc.CheckArtistSlugAvailable(adminCtx, connect.NewRequest(&managev1.CheckArtistSlugAvailableRequest{
		Slug:            draftSlug,
		ExcludeArtistId: &draft.Msg.Id,
	}))
	require.NoError(t, err)
	require.True(t, availableWhenExcludingSelf.Msg.Available)

	added, err := artistSvc.SetArtistParticipant(adminCtx, connect.NewRequest(&managev1.SetArtistParticipantRequest{
		ArtistId: draft.Msg.Id,
		MemberId: managerMemberID,
		Role:     managev1.ArtistParticipantRole_ARTIST_PARTICIPANT_ROLE_MANAGER,
	}))
	require.NoError(t, err)
	require.Equal(t, managerMemberID, added.Msg.Member.Id)
	require.Equal(t, "Artist Workflow Manager", added.Msg.Member.Nickname)
	requireResourceManagerRow(t, db, "artist_manager", "artist_id", draft.Msg.Id, managerMemberID)

	participants, err := artistSvc.ListArtistParticipants(adminCtx, connect.NewRequest(&managev1.ListArtistParticipantsRequest{ArtistId: draft.Msg.Id}))
	require.NoError(t, err)
	require.NotNil(t, findArtistParticipant(participants.Msg.Participants, adminMemberID))
	manager := findArtistParticipant(participants.Msg.Participants, managerMemberID)
	require.NotNil(t, manager)
	require.Equal(t, "Artist Workflow Manager", manager.Member.Nickname)
	require.Equal(t, managev1.ArtistParticipantRole_ARTIST_PARTICIPANT_ROLE_MANAGER, manager.Role)

	managerCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(managerID),
		MemberID:      auth.MemberID(managerMemberID),
		SessionID:     auth.SessionID(testutil.IntegrationUUID()),
		Authenticated: true,
	})
	myArtists, err := artistSvc.ListMyArtists(managerCtx, connect.NewRequest(&managev1.ListMyArtistsRequest{
		Filters: []*commonv1.FilterSpec{{
			Field: "search",
			Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
			Value: "Artist List Draft",
		}},
	}))
	require.NoError(t, err)
	requireArtistIDs(t, myArtists.Msg.Artists, draft.Msg.Id)

	removed, err := artistSvc.RemoveArtistParticipant(adminCtx, connect.NewRequest(&managev1.RemoveArtistParticipantRequest{
		ArtistId: draft.Msg.Id,
		MemberId: managerMemberID,
	}))
	require.NoError(t, err)
	require.True(t, removed.Msg.Success)
	requireNoResourceManagerRow(t, db, "artist_manager", "artist_id", draft.Msg.Id, managerMemberID)

	myArtists, err = artistSvc.ListMyArtists(managerCtx, connect.NewRequest(&managev1.ListMyArtistsRequest{}))
	require.NoError(t, err)
	requireNotArtistIDs(t, myArtists.Msg.Artists, draft.Msg.Id)
}

func artistStringPointer(value string) *string { return &value }

func requireNoResourceManagerRow(t *testing.T, db *gorm.DB, tableName, resourceIDColumn, resourceID, memberID string) {
	t.Helper()
	var count int64
	result := db.Raw(
		`SELECT COUNT(*) FROM `+tableName+` WHERE `+resourceIDColumn+` = ? AND member_id = ?`,
		resourceID,
		memberID,
	).Scan(&count)
	require.NoError(t, result.Error)
	require.Zero(t, count)
}

func requireNotArtistIDs(t *testing.T, artists []*managev1.Artist, excludedIDs ...string) {
	t.Helper()

	for _, excludedID := range excludedIDs {
		for _, artist := range artists {
			require.NotEqual(t, excludedID, artist.GetId())
		}
	}
}

func findArtistParticipant(participants []*managev1.ArtistParticipant, memberID string) *managev1.ArtistParticipant {
	for _, participant := range participants {
		if participant.GetMember().GetId() == memberID {
			return participant
		}
	}
	return nil
}
