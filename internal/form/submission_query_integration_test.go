//go:build integration

package form_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestListFormSubmissionsFiltersAndSortsIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	adminCtx, spiceDB := testutil.IntegrationAdminContext(t, db)
	admin := auth.GetUser(adminCtx)
	require.NotNil(t, admin)
	writer := apitelemetry.NewDurableWriter(db)
	service := newAuditedFormServiceForIntegration(t, db, admin.IdentityID.String(), writer, writer, spiceDB)

	from := time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC)
	before := from.Add(2 * time.Hour)
	formID := seedFormSourceLocaleBaseRowAt(t, db, from)
	rows := []model.FormSubmission{
		{ID: "00000000-0000-4000-8000-000000000001", FormID: formID, Data: []byte(`{"answer":"MiXeD a%_ value"}`), CountryCode: ptrString("US"), CreatedAt: from},
		{ID: "00000000-0000-4000-8000-000000000002", FormID: formID, Data: []byte(`{"answer":"MiXeD a%_ value"}`), CountryCode: ptrString("US"), CreatedAt: from.Add(time.Hour)},
		{ID: "00000000-0000-4000-8000-000000000003", FormID: formID, Data: []byte(`{"answer":"MiXeD a%_ value"}`), CountryCode: ptrString("US"), CreatedAt: from.Add(time.Hour)},
		{ID: "00000000-0000-4000-8000-000000000004", FormID: formID, Data: []byte(`{"answer":"MiXeD a%_ value"}`), CountryCode: ptrString("US"), CreatedAt: before},
		{ID: "00000000-0000-4000-8000-000000000005", FormID: formID, Data: []byte(`{"answer":"MiXeD a%_ value"}`), CountryCode: ptrString("CA"), CreatedAt: from.Add(30 * time.Minute)},
		{ID: "00000000-0000-4000-8000-000000000006", FormID: formID, Data: []byte(`{"answer":"MiXeD aXY value"}`), CountryCode: ptrString("US"), CreatedAt: from.Add(30 * time.Minute)},
	}
	require.NoError(t, db.Create(&rows).Error)

	search, countryCode := "mixed a%_", "us"
	ascending, err := service.ListFormSubmissions(formPersonalAccessAdminContext(t, admin), connect.NewRequest(&managev1.ListFormSubmissionsRequest{
		FormId:          formID,
		Search:          &search,
		CountryCode:     &countryCode,
		CreatedAtFrom:   timestamppb.New(from),
		CreatedAtBefore: timestamppb.New(before),
		Sorts:           []*commonv1.SortSpec{{Field: "createdAt", Order: commonv1.SortOrder_SORT_ORDER_ASC}},
		Pagination:      &commonv1.PaginationRequest{Limit: 1},
	}))
	require.NoError(t, err)
	require.Len(t, ascending.Msg.Submissions, 1)
	require.Equal(t, rows[0].ID, ascending.Msg.Submissions[0].Id)
	require.EqualValues(t, 3, ascending.Msg.Pagination.Total)
	require.True(t, ascending.Msg.Pagination.HasMore)

	descending, err := service.ListFormSubmissions(formPersonalAccessAdminContext(t, admin), connect.NewRequest(&managev1.ListFormSubmissionsRequest{
		FormId:          formID,
		Search:          &search,
		CountryCode:     &countryCode,
		CreatedAtFrom:   timestamppb.New(from),
		CreatedAtBefore: timestamppb.New(before),
		Sorts:           []*commonv1.SortSpec{{Field: "created_at", Order: commonv1.SortOrder_SORT_ORDER_DESC}},
		Pagination:      &commonv1.PaginationRequest{Limit: 10},
	}))
	require.NoError(t, err)
	require.Equal(t, []string{rows[2].ID, rows[1].ID, rows[0].ID}, []string{
		descending.Msg.Submissions[0].Id,
		descending.Msg.Submissions[1].Id,
		descending.Msg.Submissions[2].Id,
	})
	require.EqualValues(t, 3, descending.Msg.Pagination.Total)
	require.False(t, descending.Msg.Pagination.HasMore)

	bounded, err := service.ListFormSubmissions(formPersonalAccessAdminContext(t, admin), connect.NewRequest(&managev1.ListFormSubmissionsRequest{
		FormId:     formID,
		Pagination: &commonv1.PaginationRequest{Limit: 500},
	}))
	require.NoError(t, err)
	require.EqualValues(t, 100, bounded.Msg.Pagination.Limit)
	require.EqualValues(t, len(rows), bounded.Msg.Pagination.Total)
}
