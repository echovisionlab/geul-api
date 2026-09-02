//go:build integration

package integration

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/testutil"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestWorkServiceListAndMapFeaturesUsePublishedServiceCreatedWorksIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	adminID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: adminID, Name: "Public Work Admin"})
	adminMemberID := seedPublicAdminMemberIdentityLink(t, db, adminID, "Public Work Admin")
	ctx := publicLegalAdminCtx(adminMemberID, adminID)
	suffix := uuid.NewString()

	placeSvc := referencecatalog.NewMapPlaceService(
		db,
		referencecatalogadapter.NewAssets("https://cdn.example.com"),
		referencecatalogadapter.NewMemberSummaries("https://cdn.example.com"),
		publicIntegrationSpiceDB,
	)
	workPlaceGooglePlaceID := "google-place-work-" + suffix
	place, err := placeSvc.CreateMapPlace(ctx, connect.NewRequest(&managev1.CreateMapPlaceRequest{
		Name:          "Public Work Map Place " + suffix,
		Address:       "Public Work Road",
		Lat:           37.539639,
		Lng:           126.9904063,
		GooglePlaceId: &workPlaceGooglePlaceID,
	}))
	require.NoError(t, err)

	clientSvc := referencecatalog.NewClientService(
		db,
		referencecatalogadapter.NewAssets("https://cdn.example.com"),
		publicIntegrationSpiceDB,
	)
	firstClient, err := clientSvc.CreateClient(ctx, connect.NewRequest(&managev1.CreateClientRequest{
		Name:    "Public Work Client A " + suffix,
		Website: stringPtr("https://client-a.example.com"),
	}))
	require.NoError(t, err)
	secondClient, err := clientSvc.CreateClient(ctx, connect.NewRequest(&managev1.CreateClientRequest{
		Name:    "Public Work Client B " + suffix,
		Website: stringPtr("https://client-b.example.com"),
	}))
	require.NoError(t, err)
	firstLightLogoID := seedPublicClientLogoFileFixture(t, db, "public-work/"+suffix+"/client-a-light.png", "image/png")
	firstDarkLogoID := seedPublicClientLogoFileFixture(t, db, "public-work/"+suffix+"/client-a-dark.webp", "image/webp")
	_, err = clientSvc.SetClientLogo(ctx, connect.NewRequest(&managev1.SetClientLogoRequest{
		ClientId: firstClient.Msg.Id,
		FileId:   firstLightLogoID,
		Variant:  managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT,
	}))
	require.NoError(t, err)
	_, err = clientSvc.SetClientLogo(ctx, connect.NewRequest(&managev1.SetClientLogoRequest{
		ClientId: firstClient.Msg.Id,
		FileId:   firstDarkLogoID,
		Variant:  managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK,
	}))
	require.NoError(t, err)

	workSvc := workdomain.NewWorkService(
		db,
		newPublicWorkRuntimeForTest(db, "https://cdn.example.com"),
		publicIntegrationSpiceDB,
		&publicReferenceIdentityManager{identity: &auth.Identity{ID: adminID}},
		publicReferenceAsyncPublisher{},
		workdomain.WithWorkContentBlockStore(newPublicWorkContentBlockStore(t)),
		workdomain.WithWorkContentBlockMediaHydrator(extractedWorkPublicMediaHydrator{}),
		workdomain.WithWorkMemberSummaryLoader(workadapter.NewMemberSummaries(db, "https://cdn.example.com")),
	)
	published, err := workSvc.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title:     "Public Work " + suffix,
		Slug:      stringPtr("public-work-" + suffix),
		Type:      managev1.WorkType_WORK_TYPE_PORTFOLIO,
		Year:      2026,
		Month:     5,
		IsPresent: boolPtr(true),
		Document:  emptyPublicWorkDocument("en"),
	}))
	require.NoError(t, err)
	_, err = workSvc.UpdateWork(ctx, connect.NewRequest(&managev1.UpdateWorkRequest{
		Id:         published.Msg.Id,
		MapPlaceId: &place.Msg.Id,
		Clients: &managev1.WorkClientsUpdate{
			ClientIds: []string{secondClient.Msg.Id, firstClient.Msg.Id},
		},
	}))
	require.NoError(t, err)
	_, err = workSvc.UpdateWork(ctx, connect.NewRequest(&managev1.UpdateWorkRequest{
		Id: published.Msg.Id,
		Clients: &managev1.WorkClientsUpdate{
			ClientIds: []string{uuid.NewString()},
		},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	workImageFileID, _ := seedCanonicalPublicFileFixture(t, db, "featured.webp", "image/webp", "image")
	workImage, err := workSvc.SetWorkFeaturedImage(ctx, connect.NewRequest(&managev1.SetWorkFeaturedImageRequest{
		WorkId: published.Msg.Id,
		FileId: workImageFileID,
	}))
	require.NoError(t, err)
	require.Equal(t, readyPublicAssetURLForFileFixture(t, db, "https://cdn.example.com", workImageFileID), workImage.Msg.GetImageAsset().GetUrl())
	creditGroup, err := workSvc.CreateWorkCreditGroup(ctx, connect.NewRequest(&managev1.CreateWorkCreditGroupRequest{
		WorkId: published.Msg.Id,
		Name:   "Public Work Credit Group " + suffix,
	}))
	require.NoError(t, err)
	creditGroupID := creditGroup.Msg.Id
	creditName := "Public Work Credit " + suffix
	_, err = workSvc.AddWorkCredit(ctx, connect.NewRequest(&managev1.AddWorkCreditRequest{
		WorkId:  published.Msg.Id,
		GroupId: &creditGroupID,
		Name:    &creditName,
	}))
	require.NoError(t, err)
	_, err = workSvc.PublishWork(ctx, connect.NewRequest(&managev1.PublishWorkRequest{Id: published.Msg.Id}))
	require.NoError(t, err)

	groupedA, err := workSvc.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title:     "Grouped Public Work A " + suffix,
		Slug:      stringPtr("grouped-public-work-a-" + suffix),
		Type:      managev1.WorkType_WORK_TYPE_PORTFOLIO,
		Year:      2026,
		Month:     5,
		IsPresent: boolPtr(true),
		Document:  emptyPublicWorkDocument("en"),
	}))
	require.NoError(t, err)
	_, err = workSvc.UpdateWork(ctx, connect.NewRequest(&managev1.UpdateWorkRequest{
		Id:         groupedA.Msg.Id,
		MapPlaceId: &place.Msg.Id,
	}))
	require.NoError(t, err)
	_, err = workSvc.PublishWork(ctx, connect.NewRequest(&managev1.PublishWorkRequest{Id: groupedA.Msg.Id}))
	require.NoError(t, err)

	groupedB, err := workSvc.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title:     "Grouped Public Work B " + suffix,
		Slug:      stringPtr("grouped-public-work-b-" + suffix),
		Type:      managev1.WorkType_WORK_TYPE_PORTFOLIO,
		Year:      2026,
		Month:     5,
		IsPresent: boolPtr(true),
		Document:  emptyPublicWorkDocument("en"),
	}))
	require.NoError(t, err)
	_, err = workSvc.UpdateWork(ctx, connect.NewRequest(&managev1.UpdateWorkRequest{
		Id:         groupedB.Msg.Id,
		MapPlaceId: &place.Msg.Id,
	}))
	require.NoError(t, err)
	_, err = workSvc.PublishWork(ctx, connect.NewRequest(&managev1.PublishWorkRequest{Id: groupedB.Msg.Id}))
	require.NoError(t, err)

	draft, err := workSvc.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title:     "Draft Work " + suffix,
		Slug:      stringPtr("draft-work-" + suffix),
		Type:      managev1.WorkType_WORK_TYPE_PORTFOLIO,
		Year:      2026,
		Month:     5,
		IsPresent: boolPtr(true),
		Document:  emptyPublicWorkDocument("en"),
	}))
	require.NoError(t, err)

	publicSvc := newExtractedPublicWorkService(t, db, "https://cdn.example.com")
	listed, err := publicSvc.List(context.Background(), connect.NewRequest(&openv1.ListWorksRequest{
		Filters: []*commonv1.FilterSpec{{
			Field: "search",
			Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
			Value: "Work " + suffix,
		}},
		Pagination: &commonv1.PaginationRequest{Limit: 10},
	}))
	require.NoError(t, err)
	require.Equal(t, int32(1), listed.Msg.GetPagination().GetTotal())
	require.Len(t, listed.Msg.Works, 1)
	require.Equal(t, published.Msg.Id, listed.Msg.Works[0].Id)
	require.Equal(t, readyPublicAssetURLForFileFixture(t, db, "https://cdn.example.com", workImageFileID), listed.Msg.Works[0].GetFeaturedImageAsset().GetUrl())
	require.NotEqual(t, draft.Msg.Id, listed.Msg.Works[0].Id)

	_, err = publicSvc.List(context.Background(), connect.NewRequest(&openv1.ListWorksRequest{
		Filters: []*commonv1.FilterSpec{{
			Field: "unknown",
			Op:    commonv1.FilterOp_FILTER_OP_EQ,
			Value: "value",
		}},
		Pagination: &commonv1.PaginationRequest{Limit: 10},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = publicSvc.List(context.Background(), connect.NewRequest(&openv1.ListWorksRequest{
		Sorts: []*commonv1.SortSpec{{
			Field: "unknown",
			Order: commonv1.SortOrder_SORT_ORDER_ASC,
		}},
		Pagination: &commonv1.PaginationRequest{Limit: 10},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = publicSvc.Get(context.Background(), connect.NewRequest(&openv1.GetWorkRequest{Slug: draft.Msg.GetSlug()}))
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	shareToken := seedPublicWorkShareLink(t, db, draft.Msg.Id)
	sharedDraft, err := publicSvc.Get(context.Background(), connect.NewRequest(&openv1.GetWorkRequest{
		Slug:       draft.Msg.GetSlug(),
		ShareToken: &shareToken,
	}))
	require.NoError(t, err)
	require.Equal(t, draft.Msg.Id, sharedDraft.Msg.Work.Id)
	require.Equal(t, openv1.WorkStatus_WORK_STATUS_DRAFT, sharedDraft.Msg.Work.Status)

	fetchedPublished, err := publicSvc.Get(context.Background(), connect.NewRequest(&openv1.GetWorkRequest{
		Slug: published.Msg.GetSlug(),
	}))
	require.NoError(t, err)
	require.Equal(t, readyPublicAssetURLForFileFixture(t, db, "https://cdn.example.com", workImageFileID), fetchedPublished.Msg.Work.GetFeaturedImageAsset().GetUrl())
	require.Len(t, fetchedPublished.Msg.Work.Clients, 2)
	require.Equal(t, secondClient.Msg.Id, fetchedPublished.Msg.Work.Clients[0].Id)
	require.Equal(t, "Public Work Client B "+suffix, fetchedPublished.Msg.Work.Clients[0].Name)
	require.Equal(t, "https://client-b.example.com", fetchedPublished.Msg.Work.Clients[0].GetWebsite())
	require.Empty(t, fetchedPublished.Msg.Work.Clients[0].GetLogoLightAsset().GetUrl())
	require.Equal(t, firstClient.Msg.Id, fetchedPublished.Msg.Work.Clients[1].Id)
	require.Equal(t, "Public Work Client A "+suffix, fetchedPublished.Msg.Work.Clients[1].Name)
	require.Equal(t, "https://client-a.example.com", fetchedPublished.Msg.Work.Clients[1].GetWebsite())
	require.Equal(t, readyPublicAssetURLForFileFixture(t, db, "https://cdn.example.com", firstLightLogoID), fetchedPublished.Msg.Work.Clients[1].GetLogoLightAsset().GetUrl())
	require.Equal(t, readyPublicAssetURLForFileFixture(t, db, "https://cdn.example.com", firstLightLogoID), fetchedPublished.Msg.Work.Clients[1].GetLogoLightAsset().GetUrl())
	require.Equal(t, readyPublicAssetURLForFileFixture(t, db, "https://cdn.example.com", firstDarkLogoID), fetchedPublished.Msg.Work.Clients[1].GetLogoDarkAsset().GetUrl())
	require.Len(t, fetchedPublished.Msg.Work.CreditGroups, 1)
	require.Equal(t, creditGroup.Msg.Id, fetchedPublished.Msg.Work.CreditGroups[0].Id)
	require.Equal(t, creditGroup.Msg.Name, fetchedPublished.Msg.Work.CreditGroups[0].Name)
	require.Len(t, fetchedPublished.Msg.Work.Credits, 1)
	require.Equal(t, creditGroup.Msg.Id, fetchedPublished.Msg.Work.Credits[0].GetGroupId())
	require.Equal(t, creditName, fetchedPublished.Msg.Work.Credits[0].GetName())
	require.NotNil(t, fetchedPublished.Msg.Work.LocationPlace)
	require.Equal(t, workPlaceGooglePlaceID, fetchedPublished.Msg.Work.LocationPlace.GetGooglePlaceId())

	viewport := &openv1.WorkMapViewport{
		Bounds: &openv1.WorkMapBounds{
			West:  126.0,
			South: 37.0,
			East:  127.5,
			North: 38.0,
		},
		Zoom:             12,
		WidthPx:          1280,
		HeightPx:         720,
		ClusterRadiusPx:  56,
		MinClusterPoints: 2,
	}
	features, err := publicSvc.ListMapFeatures(context.Background(), connect.NewRequest(&openv1.ListWorkMapFeaturesRequest{
		Viewport: viewport,
		Filters: []*commonv1.FilterSpec{{
			Field: "search",
			Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
			Value: "Public Work " + suffix,
		}},
	}))
	require.NoError(t, err)
	require.Empty(t, features.Msg.Clusters)
	require.Len(t, features.Msg.Items, 1)
	require.Equal(t, place.Msg.Id, features.Msg.Items[0].PlaceId)
	require.Equal(t, published.Msg.Id, features.Msg.Items[0].PrimaryWorkId)
	require.Equal(t, "Public Work "+suffix, features.Msg.Items[0].PrimaryWorkTitle)

	defaultedViewportFeatures, err := publicSvc.ListMapFeatures(context.Background(), connect.NewRequest(&openv1.ListWorkMapFeaturesRequest{
		Viewport: &openv1.WorkMapViewport{
			Bounds: &openv1.WorkMapBounds{
				West:  126.0,
				South: 37.0,
				East:  127.5,
				North: 38.0,
			},
		},
		Filters: []*commonv1.FilterSpec{{
			Field: "search",
			Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
			Value: "Public Work " + suffix,
		}},
	}))
	require.NoError(t, err)
	require.Empty(t, defaultedViewportFeatures.Msg.Clusters)
	require.Len(t, defaultedViewportFeatures.Msg.Items, 1)
	require.Equal(t, published.Msg.Id, defaultedViewportFeatures.Msg.Items[0].PrimaryWorkId)

	worldViewport := &openv1.WorkMapViewport{
		Bounds: &openv1.WorkMapBounds{
			West:  -54.67106,
			South: -61.14290,
			East:  54.67106,
			North: 61.64470,
		},
		Zoom:             1.5,
		WidthPx:          1888,
		HeightPx:         800,
		ClusterRadiusPx:  56,
		MinClusterPoints: 2,
	}
	groupedFeatures, err := publicSvc.ListMapFeatures(context.Background(), connect.NewRequest(&openv1.ListWorkMapFeaturesRequest{
		Viewport: worldViewport,
		Filters: []*commonv1.FilterSpec{{
			Field: "search",
			Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
			Value: "Grouped Public Work",
		}},
	}))
	require.NoError(t, err)
	require.Empty(t, groupedFeatures.Msg.Clusters)
	require.Len(t, groupedFeatures.Msg.Items, 1)
	require.Equal(t, place.Msg.Id, groupedFeatures.Msg.Items[0].PlaceId)
	require.EqualValues(t, 2, groupedFeatures.Msg.Items[0].WorkCount)
	require.Contains(t, []string{groupedA.Msg.Id, groupedB.Msg.Id}, groupedFeatures.Msg.Items[0].PrimaryWorkId)

	antimeridianFeatures, err := publicSvc.ListMapFeatures(context.Background(), connect.NewRequest(&openv1.ListWorkMapFeaturesRequest{
		Viewport: &openv1.WorkMapViewport{
			Bounds: &openv1.WorkMapBounds{
				West:  126.5,
				South: 37.0,
				East:  -170.0,
				North: 38.0,
			},
			Zoom:             12,
			WidthPx:          1280,
			HeightPx:         720,
			ClusterRadiusPx:  56,
			MinClusterPoints: 2,
		},
		Filters: []*commonv1.FilterSpec{{
			Field: "search",
			Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
			Value: "Public Work " + suffix,
		}},
	}))
	require.NoError(t, err)
	require.Empty(t, antimeridianFeatures.Msg.Clusters)
	require.Len(t, antimeridianFeatures.Msg.Items, 1)
	require.Equal(t, published.Msg.Id, antimeridianFeatures.Msg.Items[0].PrimaryWorkId)

	emptyFeatures, err := publicSvc.ListMapFeatures(context.Background(), connect.NewRequest(&openv1.ListWorkMapFeaturesRequest{
		Viewport: worldViewport,
		Filters: []*commonv1.FilterSpec{{
			Field: "search",
			Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
			Value: "missing grouped public work " + suffix,
		}},
	}))
	require.NoError(t, err)
	require.Empty(t, emptyFeatures.Msg.Clusters)
	require.Empty(t, emptyFeatures.Msg.Items)

	_, err = publicSvc.ListMapFeatures(context.Background(), connect.NewRequest(&openv1.ListWorkMapFeaturesRequest{
		Viewport: viewport,
		Filters: []*commonv1.FilterSpec{{
			Field: "unknown",
			Op:    commonv1.FilterOp_FILTER_OP_EQ,
			Value: "value",
		}},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = publicSvc.ListMapFeatures(context.Background(), connect.NewRequest(&openv1.ListWorkMapFeaturesRequest{
		Viewport: viewport,
		Sorts: []*commonv1.SortSpec{{
			Field: "unknown",
			Order: commonv1.SortOrder_SORT_ORDER_ASC,
		}},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func boolPtr(value bool) *bool {
	return &value
}
