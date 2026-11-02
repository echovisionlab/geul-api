package programeventpublicpostgres

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func newDryRunPublicServiceDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=localhost user=geul dbname=geul sslmode=disable",
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	require.NoError(t, err)
	return db
}

func TestProgramEventPublicQueriesRejectInvalidSortsAndFilters(t *testing.T) {
	db := newDryRunPublicServiceDB(t)
	query := db.Table("program_event")

	_, err := applyProgramEventPublicSort(query, []*commonv1.SortSpec{{Field: "unknown"}})
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
