package programevent

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Stored values equal the API enum names, so every API status except
// UNSPECIFIED must round-trip; a new API value fails here until it is mapped.
func TestProgramEventStatusMappingCoversEveryAPIStatus(t *testing.T) {
	t.Parallel()
	for value, name := range managev1.ProgramEventStatus_name {
		status := managev1.ProgramEventStatus(value)
		stored, known := programEventStatusFromProto(status)
		if status == managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_UNSPECIFIED {
			if known {
				t.Fatalf("UNSPECIFIED mapped to %q", stored)
			}
			continue
		}
		if !known || string(stored) != name || programEventStatusToProto(stored) != status {
			t.Fatalf("%s maps to %q (known %t) and back to %s", name, stored, known, programEventStatusToProto(stored))
		}
	}
	if got := programEventStatusToProto("PROGRAM_EVENT_STATUS_DELETED"); got != managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_UNSPECIFIED {
		t.Fatalf("unknown stored status mapped to %s", got)
	}
}

// A series stores the event draft and published values.
func TestProgramEventSeriesStatusMappingCoversEveryAPIStatus(t *testing.T) {
	t.Parallel()
	for value, name := range managev1.ProgramEventSeriesStatus_name {
		status := managev1.ProgramEventSeriesStatus(value)
		stored, known := programEventSeriesStatusFromProto(status)
		filtered, filterKnown := seriesStatusFilterValues[name]
		if status == managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_UNSPECIFIED {
			if known || filterKnown {
				t.Fatalf("UNSPECIFIED mapped to %q", stored)
			}
			continue
		}
		if !known || !filterKnown || filtered != stored || programEventSeriesStatusToProto(stored) != status {
			t.Fatalf("%s maps to %q (known %t, filter %q) and back to %s", name, stored, known, filtered, programEventSeriesStatusToProto(stored))
		}
	}
	if got := programEventSeriesStatusToProto(StatusArchived); got != managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_UNSPECIFIED {
		t.Fatalf("archived series status mapped to %s", got)
	}
}

func TestProgramEventTypeStatusMappingCoversEveryAPIStatus(t *testing.T) {
	t.Parallel()
	for value, name := range managev1.ProgramEventTypeStatus_name {
		status := managev1.ProgramEventTypeStatus(value)
		stored, known := programEventTypeStatusFromProto(status)
		if status == managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_UNSPECIFIED {
			if known {
				t.Fatalf("UNSPECIFIED mapped to %q", stored)
			}
			continue
		}
		if !known || string(stored) != name || programEventTypeStatusToProto(stored) != status {
			t.Fatalf("%s maps to %q (known %t) and back to %s", name, stored, known, programEventTypeStatusToProto(stored))
		}
	}
}

// UNSPECIFIED is the Map Place default a create request leaves unset.
func TestProgramEventLocationModeMappingCoversEveryAPIMode(t *testing.T) {
	t.Parallel()
	for value, name := range managev1.ProgramEventLocationMode_name {
		mode := managev1.ProgramEventLocationMode(value)
		stored := locationModeFromProto(mode)
		if mode == managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_UNSPECIFIED {
			if stored != LocationModeMapPlace {
				t.Fatalf("UNSPECIFIED mapped to %q", stored)
			}
			continue
		}
		if string(stored) != name || locationModeToProto(stored) != mode {
			t.Fatalf("%s maps to %q and back to %s", name, stored, locationModeToProto(stored))
		}
	}
	if got := locationModeFromProto(managev1.ProgramEventLocationMode(99)); got != "" {
		t.Fatalf("unknown API mode mapped to %q", got)
	}
}
