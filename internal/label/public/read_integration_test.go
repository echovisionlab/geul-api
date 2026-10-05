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

	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	labelpublic "github.com/echovisionlab/geul-api/internal/label/public"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	artistlabelpublictest "github.com/echovisionlab/geul-api/internal/testutil/artistlabelpublic"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestLabelPublicReadsHideDraftsAndExposePublishedRelationsIntegration(t *testing.T) {
	fixture := artistlabelpublictest.New(t)
	db := fixture.DB
	adminID := fixture.AdminID
	ctx := fixture.Context
	suffix := fixture.Suffix
	publicLabelSvc := fixture.LabelService
	manageShareSvc := fixture.ShareLinkService
	parentLabel := fixture.ParentLabel
	mainLabel := fixture.MainLabel
	draftLabel := fixture.DraftLabel
	darkOnlyLabel := fixture.DarkOnlyLabel
	bareReleaseLabel := fixture.BareReleaseLabel
	mainLabelDescription := fixture.MainLabelDescription
	labelLightLogoID := fixture.LabelLightLogoID
	labelDarkLogoID := fixture.LabelDarkLogoID
	darkOnlyLogoID := fixture.DarkOnlyLogoID
	mainLabelOgAssetID := fixture.MainLabelOGAssetID
	mainLabelOgAssetURL := fixture.MainLabelOGAssetURL
	mainArtist := fixture.MainArtist
	otherArtist := fixture.OtherArtist
	bareReleaseArtist := fixture.BareReleaseArtist
	artistImageFileID := fixture.ArtistImageFileID
	releaseArtworkFileID := fixture.ReleaseArtworkFileID

	list, err := publicLabelSvc.List(context.Background(), connect.NewRequest(&openv1.ListLabelsRequest{
		Filters: []*commonv1.FilterSpec{{
			Field: "search",
			Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
			Value: "Public Main Label " + suffix,
		}},
		Pagination: &commonv1.PaginationRequest{Limit: 1},
	}))
	require.NoError(t, err)
	require.Len(t, list.Msg.Labels, 1)
	require.Equal(t, mainLabel.Id, list.Msg.Labels[0].Id)
	require.Equal(t, "https://example.com/public-main-label-"+suffix, list.Msg.Labels[0].GetWebsite())
	require.Equal(t, artistlabelpublictest.AssetURL(t, db, labelLightLogoID), list.Msg.Labels[0].GetImageLightAsset().GetUrl())
	require.Equal(t, artistlabelpublictest.AssetURL(t, db, labelLightLogoID), list.Msg.Labels[0].GetImageLightAsset().GetUrl())
	require.Equal(t, artistlabelpublictest.AssetURL(t, db, labelDarkLogoID), list.Msg.Labels[0].GetImageDarkAsset().GetUrl())
	require.Equal(t, mainLabelOgAssetURL, list.Msg.Labels[0].GetOgAsset().GetUrl())
	require.EqualValues(t, 1, list.Msg.Pagination.Total)
	require.False(t, list.Msg.Pagination.HasMore)

	listByIDs, err := publicLabelSvc.List(context.Background(), connect.NewRequest(&openv1.ListLabelsRequest{
		Filters: []*commonv1.FilterSpec{{
			Field:  "id",
			Op:     commonv1.FilterOp_FILTER_OP_IN,
			Values: []string{mainLabel.Id, darkOnlyLabel.Id},
		}},
		Pagination: &commonv1.PaginationRequest{Limit: 2},
	}))
	require.NoError(t, err)
	require.Len(t, listByIDs.Msg.Labels, 2)
	require.ElementsMatch(t, []string{mainLabel.Id, darkOnlyLabel.Id}, []string{
		listByIDs.Msg.Labels[0].Id,
		listByIDs.Msg.Labels[1].Id,
	})

	darkOnlyList, err := publicLabelSvc.List(context.Background(), connect.NewRequest(&openv1.ListLabelsRequest{
		Filters: []*commonv1.FilterSpec{{
			Field: "search",
			Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
			Value: "Public Dark Only Label " + suffix,
		}},
		Pagination: &commonv1.PaginationRequest{Limit: 1},
	}))
	require.NoError(t, err)
	require.Len(t, darkOnlyList.Msg.Labels, 1)
	require.Equal(t, darkOnlyLabel.Id, darkOnlyList.Msg.Labels[0].Id)
	require.Equal(t, artistlabelpublictest.AssetURL(t, db, darkOnlyLogoID), darkOnlyList.Msg.Labels[0].GetImageDarkAsset().GetUrl())
	require.Empty(t, darkOnlyList.Msg.Labels[0].GetImageLightAsset().GetUrl())
	require.Equal(t, artistlabelpublictest.AssetURL(t, db, darkOnlyLogoID), darkOnlyList.Msg.Labels[0].GetImageDarkAsset().GetUrl())

	_, err = publicLabelSvc.List(context.Background(), connect.NewRequest(&openv1.ListLabelsRequest{
		Filters: []*commonv1.FilterSpec{{
			Field: "unknown",
			Op:    commonv1.FilterOp_FILTER_OP_EQ,
			Value: "value",
		}},
		Pagination: &commonv1.PaginationRequest{Limit: 10},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = publicLabelSvc.List(context.Background(), connect.NewRequest(&openv1.ListLabelsRequest{
		Sorts: []*commonv1.SortSpec{{
			Field: "unknown",
			Order: commonv1.SortOrder_SORT_ORDER_ASC,
		}},
		Pagination: &commonv1.PaginationRequest{Limit: 10},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = publicLabelSvc.Get(context.Background(), connect.NewRequest(&openv1.GetLabelRequest{Slug: draftLabel.GetSlug()}))
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	adminDraftLabel, err := publicLabelSvc.Get(testutil.PostIntegrationContext(adminID), connect.NewRequest(&openv1.GetLabelRequest{Slug: draftLabel.GetSlug()}))
	require.NoError(t, err)
	require.Equal(t, draftLabel.Id, adminDraftLabel.Msg.Label.Id)

	managerRuntime := labeladapter.NewPublicRuntime(
		db, "https://cdn.example.com", fixture.SpiceDB,
		testutil.NewCreativeContentStore(t, fixture.SpiceDB),
	)
	managerLabelSvc := labelpublic.NewLabelService(
		db,
		labelpublic.Dependencies{
			Content: managerRuntime,
			Draft:   managerRuntime,
			Media:   managerRuntime,
		},
	)
	managerReq := connect.NewRequest(&openv1.GetLabelRequest{Slug: draftLabel.GetSlug()})
	managerCtx := testutil.PostIntegrationContext(adminID)
	managerDraftLabel, err := managerLabelSvc.Get(managerCtx, managerReq)
	require.NoError(t, err)
	require.Equal(t, draftLabel.Id, managerDraftLabel.Msg.Label.Id)

	labelShareLink, err := manageShareSvc.CreateShareLink(ctx, connect.NewRequest(&managev1.CreateShareLinkRequest{
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_LABEL,
		EntityId:   draftLabel.Id,
	}))
	require.NoError(t, err)
	sharedLabel, err := publicLabelSvc.Get(context.Background(), connect.NewRequest(&openv1.GetLabelRequest{
		Slug:       draftLabel.GetSlug(),
		ShareToken: &labelShareLink.Msg.ShareLink.Token,
	}))
	require.NoError(t, err)
	require.Equal(t, draftLabel.Id, sharedLabel.Msg.Label.Id)
	require.Equal(t, openv1.LabelStatus_LABEL_STATUS_DRAFT, sharedLabel.Msg.Label.Status)

	password := "label-share-password"
	protectedLabelShareLink, err := manageShareSvc.CreateShareLink(ctx, connect.NewRequest(&managev1.CreateShareLinkRequest{
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_LABEL,
		EntityId:   draftLabel.Id,
		Password:   &password,
	}))
	require.NoError(t, err)
	for _, supplied := range []string{"", "wrong-password"} {
		_, err = publicLabelSvc.Get(context.Background(), connect.NewRequest(&openv1.GetLabelRequest{
			Slug:          draftLabel.GetSlug(),
			ShareToken:    &protectedLabelShareLink.Msg.ShareLink.Token,
			SharePassword: &supplied,
		}))
		require.Error(t, err)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	}
	protectedLabel, err := publicLabelSvc.Get(context.Background(), connect.NewRequest(&openv1.GetLabelRequest{
		Slug:          draftLabel.GetSlug(),
		ShareToken:    &protectedLabelShareLink.Msg.ShareLink.Token,
		SharePassword: &password,
	}))
	require.NoError(t, err)
	require.Equal(t, draftLabel.Id, protectedLabel.Msg.Label.Id)

	expiredLabelShareLink, err := manageShareSvc.CreateShareLink(ctx, connect.NewRequest(&managev1.CreateShareLinkRequest{
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_LABEL,
		EntityId:   draftLabel.Id,
		ExpiresAt:  timestamppb.New(time.Now().UTC().Add(time.Hour)),
	}))
	require.NoError(t, err)
	expiredNow := time.Now().UTC()
	require.NoError(t, db.Model(&model.ShareLink{}).
		Where("token = ?", expiredLabelShareLink.Msg.ShareLink.Token).
		Updates(structured.Fields{
			"created_at": expiredNow.Add(-2 * time.Hour),
			"expires_at": expiredNow.Add(-time.Hour),
		}).Error)
	_, err = publicLabelSvc.Get(context.Background(), connect.NewRequest(&openv1.GetLabelRequest{
		Slug:       draftLabel.GetSlug(),
		ShareToken: &expiredLabelShareLink.Msg.ShareLink.Token,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	got, err := publicLabelSvc.Get(context.Background(), connect.NewRequest(&openv1.GetLabelRequest{Slug: mainLabel.GetSlug()}))
	require.NoError(t, err)
	require.Equal(t, mainLabel.Id, got.Msg.Label.Id)
	require.Equal(t, mainLabel.Name, got.Msg.Label.Name)
	require.Equal(t, openv1.LabelStatus_LABEL_STATUS_PUBLISHED, got.Msg.Label.Status)
	require.Equal(t, mainLabelDescription, artistlabelpublictest.LocalizedDocumentText(got.Msg.Label.GetDocument()))
	require.Equal(t, artistlabelpublictest.AssetURL(t, db, labelLightLogoID), got.Msg.Label.GetImageLightAsset().GetUrl())
	require.Equal(t, artistlabelpublictest.AssetURL(t, db, labelLightLogoID), got.Msg.Label.GetImageLightAsset().GetUrl())
	require.Equal(t, artistlabelpublictest.AssetURL(t, db, labelDarkLogoID), got.Msg.Label.GetImageDarkAsset().GetUrl())
	require.Equal(t, "KR", got.Msg.Label.GetCountryCode())
	require.Equal(t, "https://example.com/public-main-label-"+suffix, got.Msg.Label.GetWebsite())
	require.Equal(t, "https://social.example.com/public-main-label-"+suffix, got.Msg.Label.SocialLinks["homepage"])
	require.Equal(t, mainLabelOgAssetID, got.Msg.Label.GetOgAsset().GetAssetId())
	require.Equal(t, mainLabelOgAssetURL, got.Msg.Label.GetOgAsset().GetUrl())
	require.NotNil(t, got.Msg.Label.ParentLabel)
	require.Equal(t, parentLabel.Id, got.Msg.Label.ParentLabel.Id)
	require.Empty(t, got.Msg.Label.ParentLabel.GetOgAsset().GetUrl())
	require.EqualValues(t, 1, got.Msg.Label.ArtistCount)
	require.EqualValues(t, 1, got.Msg.Label.ReleaseCount)
	require.Len(t, got.Msg.Label.Artists, 1)
	require.Equal(t, mainArtist.Id, got.Msg.Label.Artists[0].Id)
	require.Equal(t, artistlabelpublictest.AssetURL(t, db, artistImageFileID), got.Msg.Label.Artists[0].GetImageAsset().GetUrl())
	require.Len(t, got.Msg.Label.Releases, 1)
	require.Equal(t, fixture.Release.Id, got.Msg.Label.Releases[0].Id)
	require.Equal(t, artistlabelpublictest.AssetURL(t, db, releaseArtworkFileID), got.Msg.Label.Releases[0].GetArtworkAsset().GetUrl())
	require.Len(t, got.Msg.Label.Releases[0].Artists, 2)
	require.Equal(t, mainArtist.Id, got.Msg.Label.Releases[0].Artists[0].Id)
	require.Equal(t, otherArtist.Id, got.Msg.Label.Releases[0].Artists[1].Id)

	gotByID, err := publicLabelSvc.Get(context.Background(), connect.NewRequest(&openv1.GetLabelRequest{Slug: mainLabel.Id}))
	require.NoError(t, err)
	require.Equal(t, mainLabel.Id, gotByID.Msg.Label.Id)

	darkOnlyGot, err := publicLabelSvc.Get(context.Background(), connect.NewRequest(&openv1.GetLabelRequest{Slug: darkOnlyLabel.GetSlug()}))
	require.NoError(t, err)
	require.Equal(t, artistlabelpublictest.AssetURL(t, db, darkOnlyLogoID), darkOnlyGot.Msg.Label.GetImageDarkAsset().GetUrl())
	require.Empty(t, darkOnlyGot.Msg.Label.GetImageLightAsset().GetUrl())
	require.Equal(t, artistlabelpublictest.AssetURL(t, db, darkOnlyLogoID), darkOnlyGot.Msg.Label.GetImageDarkAsset().GetUrl())

	_, err = publicLabelSvc.Get(context.Background(), connect.NewRequest(&openv1.GetLabelRequest{Slug: uuid.NewString()}))
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	bareLabelReleases, err := publicLabelSvc.Get(context.Background(), connect.NewRequest(&openv1.GetLabelRequest{
		Slug: bareReleaseLabel.Id,
	}))
	require.NoError(t, err)
	require.Len(t, bareLabelReleases.Msg.Label.Releases, 1)
	require.Equal(t, fixture.BareReleaseID, bareLabelReleases.Msg.Label.Releases[0].Id)
	require.Empty(t, bareLabelReleases.Msg.Label.Releases[0].GetArtworkAsset().GetUrl())
	require.Len(t, bareLabelReleases.Msg.Label.Releases[0].Artists, 1)
	require.Equal(t, bareReleaseArtist.Id, bareLabelReleases.Msg.Label.Releases[0].Artists[0].Id)
}
