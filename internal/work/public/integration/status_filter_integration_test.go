//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestWorkPublicListAndMapStatusFiltersNeverExposeDraftIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	adminID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: adminID, Name: "Public Work Status Admin"})
	adminMemberID := seedPublicAdminMemberIdentityLink(t, db, adminID, "Public Work Status Admin")
	ctx := publicLegalAdminCtx(adminMemberID, adminID)
	manageWork := newPublicWorkManageService(t, db, adminID)
	suffix := uuid.NewString()
	now := time.Now().UTC()
	place := model.MapPlace{
		ID:        uuid.NewString(),
		Name:      "Public Work Status Place " + suffix,
		Address:   "Public Work Status Road",
		Lat:       37.55,
		Lng:       126.99,
		CreatedAt: now,
		UpdatedAt: now,
	}
	require.NoError(t, db.Create(&place).Error)

	createWork := func(label string) string {
		t.Helper()
		created, err := manageWork.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
			Title:     "Public Work Status " + label + " " + suffix,
			Type:      managev1.WorkType_WORK_TYPE_ARTICLE,
			Year:      2026,
			Month:     8,
			IsPresent: boolPtr(true),
			Document:  emptyPublicWorkDocument("en"),
		}))
		require.NoError(t, err)
		_, err = manageWork.UpdateWork(ctx, connect.NewRequest(&managev1.UpdateWorkRequest{
			Id:         created.Msg.Id,
			MapPlaceId: &place.ID,
		}))
		require.NoError(t, err)
		return created.Msg.Id
	}

	draftID := createWork("Draft")
	publishedID := createWork("Published")
	_, err := manageWork.PublishWork(ctx, connect.NewRequest(&managev1.PublishWorkRequest{Id: publishedID}))
	require.NoError(t, err)
	archivedID := createWork("Archived")
	_, err = manageWork.PublishWork(ctx, connect.NewRequest(&managev1.PublishWorkRequest{Id: archivedID}))
	require.NoError(t, err)
	require.NoError(t, db.Table("work").Where("id = ?", archivedID).
		Update("status", managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()).Error)

	publicWork := newExtractedPublicWorkService(t, db, "")
	searchFilter := &commonv1.FilterSpec{
		Field: "search",
		Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
		Value: suffix,
	}
	viewport := &openv1.WorkMapViewport{
		Bounds: &openv1.WorkMapBounds{
			West:  126,
			South: 37,
			East:  127,
			North: 38,
		},
		Zoom:             12,
		WidthPx:          1280,
		HeightPx:         720,
		ClusterRadiusPx:  56,
		MinClusterPoints: 2,
	}

	assertPublicResults := func(t *testing.T, statusFilter *commonv1.FilterSpec, expectedIDs ...string) {
		t.Helper()
		filters := []*commonv1.FilterSpec{searchFilter}
		if statusFilter != nil {
			filters = append(filters, statusFilter)
		}

		listed, listErr := publicWork.List(context.Background(), connect.NewRequest(&openv1.ListWorksRequest{
			Filters: filters,
		}))
		require.NoError(t, listErr)
		listedIDs := make([]string, 0, len(listed.Msg.Works))
		for _, item := range listed.Msg.Works {
			listedIDs = append(listedIDs, item.Id)
		}
		require.ElementsMatch(t, expectedIDs, listedIDs)
		require.NotContains(t, listedIDs, draftID)

		features, mapErr := publicWork.ListMapFeatures(context.Background(), connect.NewRequest(&openv1.ListWorkMapFeaturesRequest{
			Viewport: viewport,
			Filters:  filters,
		}))
		require.NoError(t, mapErr)
		require.Empty(t, features.Msg.Clusters)
		if len(expectedIDs) == 0 {
			require.Empty(t, features.Msg.Items)
			return
		}
		require.Len(t, features.Msg.Items, 1)
		require.EqualValues(t, len(expectedIDs), features.Msg.Items[0].WorkCount)
		if len(expectedIDs) == 1 {
			require.Equal(t, expectedIDs[0], features.Msg.Items[0].PrimaryWorkId)
		}
	}

	assertPublicResults(t, nil, publishedID, archivedID)
	for _, test := range []struct {
		name        string
		filter      *commonv1.FilterSpec
		expectedIDs []string
	}{
		{
			name: "published",
			filter: &commonv1.FilterSpec{
				Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ,
				Value: managev1.WorkStatus_WORK_STATUS_PUBLISHED.String(),
			},
			expectedIDs: []string{publishedID},
		},
		{
			name: "archived",
			filter: &commonv1.FilterSpec{
				Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ,
				Value: managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(),
			},
			expectedIDs: []string{archivedID},
		},
		{
			name: "not published",
			filter: &commonv1.FilterSpec{
				Field: "status", Op: commonv1.FilterOp_FILTER_OP_NEQ,
				Value: managev1.WorkStatus_WORK_STATUS_PUBLISHED.String(),
			},
			expectedIDs: []string{archivedID},
		},
		{
			name: "not archived",
			filter: &commonv1.FilterSpec{
				Field: "status", Op: commonv1.FilterOp_FILTER_OP_NOT_IN,
				Values: []string{managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()},
			},
			expectedIDs: []string{publishedID},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertPublicResults(t, test.filter, test.expectedIDs...)
		})
	}

	for _, test := range []struct {
		name   string
		filter *commonv1.FilterSpec
	}{
		{
			name: "draft equals",
			filter: &commonv1.FilterSpec{
				Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ,
				Value: managev1.WorkStatus_WORK_STATUS_DRAFT.String(),
			},
		},
		{
			name: "draft not equals",
			filter: &commonv1.FilterSpec{
				Field: "status", Op: commonv1.FilterOp_FILTER_OP_NEQ,
				Value: managev1.WorkStatus_WORK_STATUS_DRAFT.String(),
			},
		},
		{
			name: "published and draft",
			filter: &commonv1.FilterSpec{
				Field: "status", Op: commonv1.FilterOp_FILTER_OP_IN,
				Values: []string{
					managev1.WorkStatus_WORK_STATUS_PUBLISHED.String(),
					managev1.WorkStatus_WORK_STATUS_DRAFT.String(),
				},
			},
		},
	} {
		t.Run(test.name+" rejected", func(t *testing.T) {
			filters := []*commonv1.FilterSpec{searchFilter, test.filter}
			_, listErr := publicWork.List(context.Background(), connect.NewRequest(&openv1.ListWorksRequest{
				Filters: filters,
			}))
			require.Error(t, listErr)
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(listErr))

			_, mapErr := publicWork.ListMapFeatures(context.Background(), connect.NewRequest(&openv1.ListWorkMapFeaturesRequest{
				Viewport: viewport,
				Filters:  filters,
			}))
			require.Error(t, mapErr)
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(mapErr))
		})
	}
}
