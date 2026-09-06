//go:build integration

package public_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	artistpublic "github.com/echovisionlab/geul-api/internal/artist/public"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	artistlabelpublictest "github.com/echovisionlab/geul-api/internal/testutil/artistlabelpublic"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestArtistPublicReadsHideDraftsAndExposePublishedRelationsIntegration(t *testing.T) {
	fixture := artistlabelpublictest.New(t)
	db := fixture.DB
	adminID := fixture.AdminID
	ctx := fixture.Context
	suffix := fixture.Suffix
	publicArtistSvc := fixture.ArtistService
	manageShareSvc := fixture.ShareLinkService
	parentLabel := fixture.ParentLabel
	mainLabel := fixture.MainLabel
	mainArtist := fixture.MainArtist
	otherArtist := fixture.OtherArtist
	bareReleaseArtist := fixture.BareReleaseArtist
	draftArtist := fixture.DraftArtist
	mainArtistBio := fixture.MainArtistBio
	artistImageFileID := fixture.ArtistImageFileID
	mainArtistOgAssetID := fixture.MainArtistOGAssetID
	mainArtistOgAssetURL := fixture.MainArtistOGAssetURL
	labelLightLogoID := fixture.LabelLightLogoID
	releaseArtworkFileID := fixture.ReleaseArtworkFileID
	workSummary := fixture.WorkSummary
	workImageFileID := fixture.WorkImageFileID

	list, err := publicArtistSvc.List(context.Background(), connect.NewRequest(&openv1.ListArtistsRequest{
		Filters: []*commonv1.FilterSpec{
			{
				Field: "search",
				Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
				Value: "Public Main Artist " + suffix,
			},
			{
				Field: "label_id",
				Op:    commonv1.FilterOp_FILTER_OP_EQ,
				Value: mainLabel.Id,
			},
		},
		Pagination: &commonv1.PaginationRequest{Limit: 1},
	}))
	require.NoError(t, err)
	require.Len(t, list.Msg.Artists, 1)
	require.Equal(t, mainArtist.Id, list.Msg.Artists[0].Id)
	require.Equal(t, mainArtistOgAssetURL, list.Msg.Artists[0].GetOgAsset().GetUrl())
	require.EqualValues(t, 1, list.Msg.Pagination.Total)
	require.False(t, list.Msg.Pagination.HasMore)

	listByLabelSet, err := publicArtistSvc.List(context.Background(), connect.NewRequest(&openv1.ListArtistsRequest{
		Filters: []*commonv1.FilterSpec{
			{
				Field:  "label_id",
				Op:     commonv1.FilterOp_FILTER_OP_IN,
				Values: []string{parentLabel.Id, mainLabel.Id},
			},
			{
				Field: "search",
				Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
				Value: "Public Main Artist " + suffix,
			},
		},
		Sorts: []*commonv1.SortSpec{
			{Field: "published_at", Order: commonv1.SortOrder_SORT_ORDER_DESC},
			{Field: "name", Order: commonv1.SortOrder_SORT_ORDER_ASC},
		},
	}))
	require.NoError(t, err)
	require.Len(t, listByLabelSet.Msg.Artists, 1)
	require.Equal(t, mainArtist.Id, listByLabelSet.Msg.Artists[0].Id)

	_, err = publicArtistSvc.List(context.Background(), connect.NewRequest(&openv1.ListArtistsRequest{
		Filters: []*commonv1.FilterSpec{{
			Field: "label_id",
			Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
			Value: mainLabel.Id,
		}},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = publicArtistSvc.List(context.Background(), connect.NewRequest(&openv1.ListArtistsRequest{
		Filters: []*commonv1.FilterSpec{{
			Field: "unknown",
			Op:    commonv1.FilterOp_FILTER_OP_EQ,
			Value: "value",
		}},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = publicArtistSvc.List(context.Background(), connect.NewRequest(&openv1.ListArtistsRequest{
		Sorts: []*commonv1.SortSpec{{
			Field: "created_at",
			Order: commonv1.SortOrder_SORT_ORDER_ASC,
		}},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = publicArtistSvc.Get(context.Background(), connect.NewRequest(&openv1.GetArtistRequest{Slug: draftArtist.GetSlug()}))
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	adminDraftArtist, err := publicArtistSvc.Get(testutil.PostIntegrationContext(adminID), connect.NewRequest(&openv1.GetArtistRequest{Slug: draftArtist.GetSlug()}))
	require.NoError(t, err)
	require.Equal(t, draftArtist.Id, adminDraftArtist.Msg.Artist.Id)

	managerArtistSvc := artistpublic.NewArtistService(
		db, fixture.SpiceDB, fixture.ArtistMedia,
		artistpublic.WithArtistContentBlockStore(testutil.NewCreativeContentStore(t, fixture.SpiceDB)),
	)
	managerReq := connect.NewRequest(&openv1.GetArtistRequest{Slug: draftArtist.GetSlug()})
	managerCtx := testutil.PostIntegrationContext(adminID)
	managerDraftArtist, err := managerArtistSvc.Get(managerCtx, managerReq)
	require.NoError(t, err)
	require.Equal(t, draftArtist.Id, managerDraftArtist.Msg.Artist.Id)

	artistShareLink, err := manageShareSvc.CreateShareLink(ctx, connect.NewRequest(&managev1.CreateShareLinkRequest{
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_ARTIST,
		EntityId:   draftArtist.Id,
	}))
	require.NoError(t, err)
	sharedArtist, err := publicArtistSvc.Get(context.Background(), connect.NewRequest(&openv1.GetArtistRequest{
		Slug:       draftArtist.GetSlug(),
		ShareToken: &artistShareLink.Msg.ShareLink.Token,
	}))
	require.NoError(t, err)
	require.Equal(t, draftArtist.Id, sharedArtist.Msg.Artist.Id)
	require.Equal(t, openv1.ArtistStatus_ARTIST_STATUS_DRAFT, sharedArtist.Msg.Artist.Status)
	require.NotEmpty(t, artistlabelpublictest.LocalizedDocumentText(sharedArtist.Msg.Artist.GetDocument()))

	expiredArtistShareLink, err := manageShareSvc.CreateShareLink(ctx, connect.NewRequest(&managev1.CreateShareLinkRequest{
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_ARTIST,
		EntityId:   draftArtist.Id,
		ExpiresAt:  timestamppb.New(time.Now().UTC().Add(time.Hour)),
	}))
	require.NoError(t, err)
	expiredNow := time.Now().UTC()
	require.NoError(t, db.Model(&model.ShareLink{}).
		Where("token = ?", expiredArtistShareLink.Msg.ShareLink.Token).
		Updates(structured.Fields{
			"created_at": expiredNow.Add(-2 * time.Hour),
			"expires_at": expiredNow.Add(-time.Hour),
		}).Error)
	_, err = publicArtistSvc.Get(context.Background(), connect.NewRequest(&openv1.GetArtistRequest{
		Slug:       draftArtist.GetSlug(),
		ShareToken: &expiredArtistShareLink.Msg.ShareLink.Token,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	got, err := publicArtistSvc.Get(context.Background(), connect.NewRequest(&openv1.GetArtistRequest{Slug: mainArtist.GetSlug()}))
	require.NoError(t, err)
	require.Equal(t, mainArtist.Id, got.Msg.Artist.Id)
	require.Equal(t, mainArtist.Name, got.Msg.Artist.Name)
	require.Equal(t, openv1.ArtistStatus_ARTIST_STATUS_PUBLISHED, got.Msg.Artist.Status)
	require.Equal(t, mainArtistBio, artistlabelpublictest.LocalizedDocumentText(got.Msg.Artist.GetDocument()))
	require.Equal(t, artistlabelpublictest.AssetURL(t, db, artistImageFileID), got.Msg.Artist.GetImageAsset().GetUrl())
	require.Equal(t, "KR", got.Msg.Artist.GetCountryCode())
	require.Equal(t, "https://example.com/public-main-artist-"+suffix, got.Msg.Artist.GetWebsite())
	require.Equal(t, "https://social.example.com/public-main-artist-"+suffix, got.Msg.Artist.SocialLinks["homepage"])
	require.Equal(t, mainArtistOgAssetID, got.Msg.Artist.GetOgAsset().GetAssetId())
	require.Equal(t, mainArtistOgAssetURL, got.Msg.Artist.GetOgAsset().GetUrl())
	require.True(t, got.Msg.Artist.IsGroup)
	require.EqualValues(t, 1, got.Msg.Artist.ReleaseCount)
	require.EqualValues(t, 1, got.Msg.Artist.WorkCount)
	require.Len(t, got.Msg.Artist.Labels, 1)
	require.Equal(t, mainLabel.Id, got.Msg.Artist.Labels[0].Id)
	require.Equal(t, artistlabelpublictest.AssetURL(t, db, labelLightLogoID), got.Msg.Artist.Labels[0].GetImageAsset().GetUrl())

	gotByID, err := publicArtistSvc.Get(context.Background(), connect.NewRequest(&openv1.GetArtistRequest{Slug: mainArtist.Id}))
	require.NoError(t, err)
	require.Equal(t, mainArtist.Id, gotByID.Msg.Artist.Id)

	_, err = publicArtistSvc.Get(context.Background(), connect.NewRequest(&openv1.GetArtistRequest{Slug: uuid.NewString()}))
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	offset := int32(0)
	releases, err := publicArtistSvc.GetReleases(context.Background(), connect.NewRequest(&openv1.GetArtistReleasesRequest{
		ArtistId: mainArtist.Id,
		Limit:    10,
		Offset:   &offset,
		Sorts: []*commonv1.SortSpec{
			{
				Field: "title",
				Order: commonv1.SortOrder_SORT_ORDER_ASC,
			},
			{
				Field: "published_at",
				Order: commonv1.SortOrder_SORT_ORDER_DESC,
			},
		},
	}))
	require.NoError(t, err)
	require.Len(t, releases.Msg.Releases, 1)
	require.Equal(t, fixture.Release.Id, releases.Msg.Releases[0].Id)
	require.Equal(t, artistlabelpublictest.AssetURL(t, db, releaseArtworkFileID), releases.Msg.Releases[0].GetArtworkAsset().GetUrl())
	require.Len(t, releases.Msg.Releases[0].Artists, 1)
	require.Equal(t, otherArtist.Id, releases.Msg.Releases[0].Artists[0].Id)
	require.EqualValues(t, 1, releases.Msg.Total)

	_, err = publicArtistSvc.GetReleases(context.Background(), connect.NewRequest(&openv1.GetArtistReleasesRequest{
		ArtistId: mainArtist.Id,
		Sorts: []*commonv1.SortSpec{{
			Field: "created_at",
			Order: commonv1.SortOrder_SORT_ORDER_ASC,
		}},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	bareArtistReleases, err := publicArtistSvc.GetReleases(context.Background(), connect.NewRequest(&openv1.GetArtistReleasesRequest{
		ArtistId: bareReleaseArtist.Id,
		Limit:    10,
		Offset:   &offset,
	}))
	require.NoError(t, err)
	require.Len(t, bareArtistReleases.Msg.Releases, 1)
	require.Equal(t, fixture.BareReleaseID, bareArtistReleases.Msg.Releases[0].Id)
	require.Empty(t, bareArtistReleases.Msg.Releases[0].GetArtworkAsset().GetUrl())

	works, err := publicArtistSvc.GetWorks(context.Background(), connect.NewRequest(&openv1.GetArtistWorksRequest{
		ArtistId: mainArtist.Id,
		Limit:    10,
		Offset:   &offset,
		Sorts: []*commonv1.SortSpec{
			{
				Field: "title",
				Order: commonv1.SortOrder_SORT_ORDER_ASC,
			},
			{
				Field: "published_at",
				Order: commonv1.SortOrder_SORT_ORDER_DESC,
			},
		},
	}))
	require.NoError(t, err)
	require.Len(t, works.Msg.Works, 1)
	require.Equal(t, fixture.Work.Id, works.Msg.Works[0].Id)
	require.Equal(t, workSummary, works.Msg.Works[0].GetSummary())
	require.Equal(t, artistlabelpublictest.AssetURL(t, db, workImageFileID), works.Msg.Works[0].GetImageAsset().GetUrl())
	require.EqualValues(t, 1, works.Msg.Total)

	_, err = publicArtistSvc.GetWorks(context.Background(), connect.NewRequest(&openv1.GetArtistWorksRequest{
		ArtistId: mainArtist.Id,
		Sorts: []*commonv1.SortSpec{{
			Field: "created_at",
			Order: commonv1.SortOrder_SORT_ORDER_ASC,
		}},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	bareWorks, err := publicArtistSvc.GetWorks(context.Background(), connect.NewRequest(&openv1.GetArtistWorksRequest{
		ArtistId: bareReleaseArtist.Id,
		Limit:    10,
		Offset:   &offset,
	}))
	require.NoError(t, err)
	require.Len(t, bareWorks.Msg.Works, 1)
	require.Equal(t, fixture.BareWorkID, bareWorks.Msg.Works[0].Id)
	require.Empty(t, bareWorks.Msg.Works[0].GetImageAsset().GetUrl())
	require.EqualValues(t, 1, bareWorks.Msg.Total)
}
