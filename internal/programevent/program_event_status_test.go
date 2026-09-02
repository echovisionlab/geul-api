package programevent

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestProgramEventSeriesStatusStorageMapping(t *testing.T) {
	draft, err := programEventSeriesStatusStorageValue(
		managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_DRAFT,
	)
	require.NoError(t, err)
	require.Equal(t, managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT.String(), draft)

	published, err := programEventSeriesStatusStorageValue(
		managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED,
	)
	require.NoError(t, err)
	require.Equal(t, managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String(), published)

	_, err = programEventSeriesStatusStorageValue(managev1.ProgramEventSeriesStatus(99))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestManageProgramEventSeriesStatusRejectsLegacyArchive(t *testing.T) {
	require.Equal(
		t,
		managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_UNSPECIFIED,
		manageProgramEventSeriesStatus(managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String()),
	)
}

func TestProgramEventAdminFilterSupportsExactSlugResolution(t *testing.T) {
	slug, ok := ProgramEventFilterConfig.Fields["slug"]
	require.True(t, ok)
	require.Equal(t, "slug", slug.Column)
	require.Contains(t, slug.AllowedOps, commonv1.FilterOp_FILTER_OP_EQ)
}

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
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
