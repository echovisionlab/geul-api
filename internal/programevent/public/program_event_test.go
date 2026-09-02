package public

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestProgramEventPublicEnumMappings(t *testing.T) {
	require.Equal(
		t,
		openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED,
		openProgramEventStatus(openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String()),
	)
	require.Equal(
		t,
		openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_UNSPECIFIED,
		openProgramEventStatus("PROGRAM_EVENT_STATUS_DELETED"),
	)
	require.Equal(
		t,
		openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED,
		openProgramEventStatus(openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String()),
	)
	require.Equal(
		t,
		openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED,
		openProgramEventSeriesStatus(managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String()),
	)

	require.Equal(
		t,
		openv1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_HYBRID,
		openProgramEventLocationMode(managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_HYBRID.String()),
	)
	require.Equal(
		t,
		openv1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_UNSPECIFIED,
		openProgramEventLocationMode("PROGRAM_EVENT_LOCATION_MODE_BACKSTAGE"),
	)
	require.True(t, publicProgramEventLocationModeUsesMapPlace(managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_MAP_PLACE.String()))
	require.True(t, publicProgramEventLocationModeUsesMapPlace(managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_HYBRID.String()))
	require.False(t, publicProgramEventLocationModeUsesMapPlace(managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE.String()))
	require.False(t, publicProgramEventLocationModeUsesMapPlace(managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_TBA.String()))

	require.Equal(
		t,
		openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE,
		openProgramEventTypeStatus(openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE.String()),
	)
	require.Equal(
		t,
		openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_UNSPECIFIED,
		openProgramEventTypeStatus("PROGRAM_EVENT_TYPE_STATUS_DELETED"),
	)
}

func TestProgramEventTimestampProtoHandlesNilAndValues(t *testing.T) {
	require.Nil(t, timestampProto(nil))

	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	require.True(t, timestamppb.New(now).AsTime().Equal(timestampProto(&now).AsTime()))
}

func TestProgramEventRequestValidationDoesNotRequireDatabaseRows(t *testing.T) {
	db := newDryRunPublicServiceDB(t)
	query := db.Table("program_event")

	_, err := (&ProgramEventService{}).Get(context.Background(), connect.NewRequest(&openv1.GetProgramEventRequest{}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = (&ProgramEventSeriesService{}).Get(context.Background(), connect.NewRequest(&openv1.GetProgramEventSeriesRequest{}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = applyProgramEventPublicSort(query, []*commonv1.SortSpec{{Field: "unknown"}})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	invalidFilters := []*commonv1.FilterSpec{
		{Field: "time_window", Op: commonv1.FilterOp_FILTER_OP_IN, Values: []string{"all"}},
		{Field: "time_window", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "soon"},
		{Field: "type_slug", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "type"},
		{Field: "series_slug", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "series"},
		{Field: "artist_id", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "artist"},
		{Field: "label_id", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "label"},
		{Field: "client_id", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "client"},
		{Field: "status", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String()},
	}
	for _, filter := range invalidFilters {
		_, err := applyPublicProgramEventFilters(query, []*commonv1.FilterSpec{filter})
		require.Error(t, err, "field %s should reject op %s", filter.GetField(), filter.GetOp())
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	}

	_, err = applyPublicProgramEventFilters(query, []*commonv1.FilterSpec{
		{Field: "map_place_id", Op: commonv1.FilterOp_FILTER_OP_IS_NULL},
	})
	require.NoError(t, err)

	_, err = applySimpleProgramEventSeriesFilters(query, []*commonv1.FilterSpec{
		{Field: "search", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "series"},
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = applySimpleProgramEventSeriesFilters(query, []*commonv1.FilterSpec{
		{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String()},
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = applySimpleProgramEventTypeFilters(query, []*commonv1.FilterSpec{
		{Field: "search", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "type"},
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = applySimpleProgramEventTypeFilters(query, []*commonv1.FilterSpec{
		{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE.String()},
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestProgramEventSeriesCopyIsLocaleNeutral(t *testing.T) {
	summary := "One summary"
	series := &model.ProgramEventSeries{ID: "series-1", Title: "One title", Summary: &summary, Slug: "one-title"}
	service := &ProgramEventSeriesService{}

	korean, err := service.toProtoProgramEventSeries(t.Context(), series, "ko")
	require.NoError(t, err)
	german, err := service.toProtoProgramEventSeries(t.Context(), series, "de")
	require.NoError(t, err)

	require.Equal(t, korean.Title, german.Title)
	require.Equal(t, korean.GetSummary(), german.GetSummary())
}
