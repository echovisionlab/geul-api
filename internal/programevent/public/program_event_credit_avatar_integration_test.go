//go:build integration

package public

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type publicEventCreditAvatarMembers struct {
	ids       []string
	summaries map[string]*commonv1.MemberSummary
}

func (m *publicEventCreditAvatarMembers) LoadPublicCreditMemberSummaries(_ context.Context, ids []string) (map[string]*commonv1.MemberSummary, error) {
	m.ids = append([]string(nil), ids...)
	return m.summaries, nil
}

func seedPublicCreditAvatarArtist(t *testing.T, db *gorm.DB, published bool) string {
	t.Helper()
	id, documentID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec("INSERT INTO content_document (id, profile) VALUES (?::uuid, 'compact')", documentID).Error)
	status := managev1.ArtistStatus_ARTIST_STATUS_DRAFT.String()
	if published {
		status = managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String()
	}
	slug := "credit-artist-" + id
	require.NoError(t, db.Create(&model.Artist{ID: id, ContentDocumentID: &documentID, Slug: &slug, SourceLocale: "en", Status: status}).Error)
	require.NoError(t, db.Exec("INSERT INTO artist_translation (entity_id, locale, title) VALUES (?::uuid, 'en', 'Credit artist')", id).Error)
	return id
}

func TestPublicProgramEventCreditAvatarsIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	eventID := seedPublicProgramEventListQueryFixtures(t, db, 1)[0].eventID
	role, description, fallback := "performer", "Credit description", "Fallback name"
	readyFileID, readyAssetID := seedCanonicalPublicFileFixture(t, db, "credit-avatar.webp", "image/webp", "image")
	nonReadyFileID, nonReadyAssetID := seedCanonicalPublicFileFixture(t, db, "credit-pending.webp", "image/webp", "image")
	require.NoError(t, db.Model(&model.PublicAsset{}).Where("id = ?", nonReadyAssetID).Update("status", model.PublicAssetStatusAllocated).Error)
	missingFileID, missingAssetID := seedCanonicalPublicFileFixture(t, db, "credit-missing.webp", "image/webp", "image")
	require.NoError(t, db.Delete(&model.PublicAsset{}, "id = ?", missingAssetID).Error)
	wrongKindFileID, _ := seedCanonicalPublicFileFixture(t, db, "credit-poster.webp", "image/webp", "poster")
	readyArtistID := seedPublicCreditAvatarArtist(t, db, true)
	for order, fileID := range []string{readyFileID, nonReadyFileID} {
		require.NoError(t, db.Create(&model.ArtistFile{ArtistID: readyArtistID, FileID: fileID, SortOrder: order}).Error)
	}
	artists := []string{readyArtistID, readyArtistID}
	for _, fileID := range []string{nonReadyFileID, missingFileID, wrongKindFileID} {
		artistID := seedPublicCreditAvatarArtist(t, db, true)
		require.NoError(t, db.Create(&model.ArtistFile{ArtistID: artistID, FileID: fileID, SortOrder: 0}).Error)
		// A ready second file must not replace the selected first file.
		require.NoError(t, db.Create(&model.ArtistFile{ArtistID: artistID, FileID: readyFileID, SortOrder: 1}).Error)
		artists = append(artists, artistID)
	}
	artists = append(artists, seedPublicCreditAvatarArtist(t, db, true))
	draftArtistID := seedPublicCreditAvatarArtist(t, db, false)
	require.NoError(t, db.Create(&model.ArtistFile{ArtistID: draftArtistID, FileID: readyFileID}).Error)
	artists = append(artists, draftArtistID)
	for index, artistID := range artists {
		require.NoError(t, db.Create(&model.ProgramEventCredit{
			ID: uuid.NewString(), EventID: eventID, ArtistID: &artistID,
			CreditRole: &role, Description: &description, SortOrder: int32(index),
		}).Error)
	}
	memberID := uuid.NewString()
	require.NoError(t, db.Create(&model.Member{ID: memberID, Nickname: "Credit member", SocialLinks: map[string]string{}}).Error)
	require.NoError(t, db.Create(&model.ProgramEventCredit{ID: uuid.NewString(), EventID: eventID, MemberID: &memberID, SortOrder: 7}).Error)
	require.NoError(t, db.Create(&model.ProgramEventCredit{ID: uuid.NewString(), EventID: eventID, DisplayName: &fallback, CreditRole: &role, SortOrder: 8}).Error)
	members := &publicEventCreditAvatarMembers{summaries: map[string]*commonv1.MemberSummary{
		memberID: {Id: memberID, Nickname: "Credit member", AvatarAsset: &commonv1.AssetRef{AssetId: "member-avatar"}},
	}}
	service := NewProgramEventService(db, newPublicProgramEventAssets(db, "https://cdn.example.com"), members)
	credits, err := service.loadPublicProgramEventCredits(t.Context(), eventID)
	require.NoError(t, err)
	require.Len(t, credits, 9)
	for index, credit := range credits[:7] {
		require.EqualValues(t, index, credit.SortOrder)
		require.Empty(t, credit.GetDisplayName())
		require.Equal(t, role, credit.GetCreditRole())
		require.Equal(t, description, credit.GetDescription())
		if index == 6 {
			require.Nil(t, credit.Artist, "unpublished artist must remain withheld")
			continue
		}
		require.Equal(t, "Credit artist", credit.Artist.Name)
		if index < 2 {
			require.Equal(t, readyAssetID, credit.Artist.GetImageAsset().GetAssetId())
			require.Equal(t, "https://cdn.example.com/asset/"+readyAssetID+"/image.webp", credit.Artist.GetImageAsset().GetUrl())
		} else {
			require.Nil(t, credit.Artist.ImageAsset)
		}
	}
	require.Equal(t, []string{memberID}, members.ids)
	require.Equal(t, members.summaries[memberID], credits[7].Member)
	require.Nil(t, credits[8].Artist)
	require.Equal(t, fallback, credits[8].GetDisplayName())
}

func TestPublicProgramEventCreditAvatarQueryBudgetIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	eventID := seedPublicProgramEventListQueryFixtures(t, db, 1)[0].eventID
	artistID := seedPublicCreditAvatarArtist(t, db, true)
	fileID, assetID := seedCanonicalPublicFileFixture(t, db, "shared-avatar.webp", "image/webp", "image")
	require.NoError(t, db.Create(&model.ArtistFile{ArtistID: artistID, FileID: fileID}).Error)
	var queries atomic.Int64
	countedDB := db.Session(&gorm.Session{Logger: publicProgramEventQueryCounter{Interface: db.Config.Logger, count: &queries}})
	service := NewProgramEventService(countedDB, newPublicProgramEventAssets(countedDB, "https://cdn.example.com"), &publicEventCreditAvatarMembers{})
	counts := make(map[int]int64)
	credits, err := service.loadPublicProgramEventCredits(t.Context(), eventID)
	require.NoError(t, err)
	require.Empty(t, credits)
	require.Equal(t, int64(1), queries.Load(), "no credits must not issue an asset query")
	for index := 0; index < 20; index++ {
		require.NoError(t, db.Create(&model.ProgramEventCredit{ID: uuid.NewString(), EventID: eventID, ArtistID: &artistID, SortOrder: int32(index)}).Error)
		if index != 0 && index != 19 {
			continue
		}
		queries.Store(0)
		credits, err := service.loadPublicProgramEventCredits(t.Context(), eventID)
		require.NoError(t, err)
		require.Len(t, credits, index+1)
		counts[index+1] = queries.Load()
		for _, credit := range credits {
			require.Equal(t, assetID, credit.Artist.GetImageAsset().GetAssetId())
		}
	}
	t.Logf("credit avatar SQL query counts: %v", counts)
	require.Equal(t, int64(2), counts[1])
	require.Equal(t, counts[1], counts[20], "shared artist credits must resolve assets in a single batch")
}
