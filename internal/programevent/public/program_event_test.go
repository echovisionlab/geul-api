package public

import (
	"context"
	"testing"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestProgramEventTimestampProtoHandlesNilAndValues(t *testing.T) {
	require.Nil(t, timestampProto(nil))

	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	require.True(t, timestamppb.New(now).AsTime().Equal(timestampProto(&now).AsTime()))
}

func TestProgramEventRequestValidationDoesNotRequireDatabaseRows(t *testing.T) {
	db := newDryRunPublicServiceDB(t)
	query := db.Table("program_event")

	_, err := NewConnectHandler(&Service{}).Get(context.Background(), connect.NewRequest(&openv1.GetProgramEventRequest{}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())

	_, err = NewSeriesConnectHandler(&Service{}).Get(context.Background(), connect.NewRequest(&openv1.GetProgramEventSeriesRequest{}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())

	_, err = applyProgramEventPublicSort(query, []*commonv1.SortSpec{{Field: "unknown"}})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())

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
		require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	}

	_, err = applyPublicProgramEventFilters(query, []*commonv1.FilterSpec{
		{Field: "map_place_id", Op: commonv1.FilterOp_FILTER_OP_IS_NULL},
	})
	require.NoError(t, err)

	_, err = applySimpleProgramEventSeriesFilters(query, []*commonv1.FilterSpec{
		{Field: "search", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "series"},
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())

	_, err = applySimpleProgramEventSeriesFilters(query, []*commonv1.FilterSpec{
		{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String()},
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())

	_, err = applySimpleProgramEventTypeFilters(query, []*commonv1.FilterSpec{
		{Field: "search", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "type"},
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())

	_, err = applySimpleProgramEventTypeFilters(query, []*commonv1.FilterSpec{
		{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE.String()},
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
}

func TestProgramEventSeriesCopyIsLocaleNeutral(t *testing.T) {
	summary := "One summary"
	series := &model.ProgramEventSeries{ID: "series-1", Title: "One title", Summary: &summary, Slug: "one-title"}
	korean := toProtoProgramEventSeries(t.Context(), unusedAssets{}, series)
	german := toProtoProgramEventSeries(t.Context(), unusedAssets{}, series)

	require.Equal(t, korean.Title, german.Title)
	require.Equal(t, korean.GetSummary(), german.GetSummary())
}
