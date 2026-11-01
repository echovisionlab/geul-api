package public

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/programevent"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/stretchr/testify/require"
)

// Stored values equal the API enum names, so every public API value except
// UNSPECIFIED must map from its stored name; a new API value fails here
// until it is mapped.
func TestProgramEventPublicStatusMappingsCoverEveryAPIValue(t *testing.T) {
	t.Parallel()
	for value, name := range openv1.ProgramEventStatus_name {
		if status := openv1.ProgramEventStatus(value); status != openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_UNSPECIFIED {
			require.Equal(t, status, openProgramEventStatus(programevent.Status(name)), name)
		}
	}
	for value, name := range openv1.ProgramEventLocationMode_name {
		if mode := openv1.ProgramEventLocationMode(value); mode != openv1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_UNSPECIFIED {
			require.Equal(t, mode, openProgramEventLocationMode(programevent.LocationMode(name)), name)
		}
	}
	for value, name := range openv1.ProgramEventTypeStatus_name {
		if status := openv1.ProgramEventTypeStatus(value); status != openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_UNSPECIFIED {
			require.Equal(t, status, openProgramEventTypeStatus(programevent.TypeStatus(name)), name)
		}
	}
	// A series stores the event draft and published values.
	require.Len(t, openv1.ProgramEventSeriesStatus_name, 3)
	require.Equal(t, openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_DRAFT, openProgramEventSeriesStatus(programevent.StatusDraft))
	require.Equal(t, openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED, openProgramEventSeriesStatus(programevent.StatusPublished))

	require.Equal(t, openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_UNSPECIFIED, openProgramEventStatus("PROGRAM_EVENT_STATUS_DELETED"))
	require.Equal(t, openv1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_UNSPECIFIED, openProgramEventLocationMode("PROGRAM_EVENT_LOCATION_MODE_BACKSTAGE"))
	require.Equal(t, openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_UNSPECIFIED, openProgramEventTypeStatus("PROGRAM_EVENT_TYPE_STATUS_DELETED"))
	require.Equal(t, openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_UNSPECIFIED, openProgramEventSeriesStatus(programevent.StatusArchived))
}
