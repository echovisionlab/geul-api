package programevent

import (
	"testing"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestMapProgramEventSeriesFiltersUsesSeriesContractAndStorageValues(t *testing.T) {
	mapped, err := mapProgramEventSeriesFilters([]*commonv1.FilterSpec{
		{
			Field: "status",
			Op:    commonv1.FilterOp_FILTER_OP_IN,
			Values: []string{
				managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_DRAFT.String(),
				managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED.String(),
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{
		managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT.String(),
		managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String(),
	}, mapped[0].Values)

	_, err = mapProgramEventSeriesFilters([]*commonv1.FilterSpec{{
		Field: "status",
		Op:    commonv1.FilterOp_FILTER_OP_EQ,
		Value: managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String(),
	}})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
}
