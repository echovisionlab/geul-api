package series

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Series statuses travel as their API enum names, so every API status except
// UNSPECIFIED must parse to the stored value of the same name; a new API
// status fails here until it is mapped.
func TestSeriesStatusParsingCoversEveryAPIStatus(t *testing.T) {
	t.Parallel()
	for value, name := range managev1.SeriesStatus_name {
		stored, known := parseSeriesStatus(name)
		if managev1.SeriesStatus(value) == managev1.SeriesStatus_SERIES_STATUS_UNSPECIFIED {
			if known {
				t.Fatalf("UNSPECIFIED parsed to %q", stored)
			}
			continue
		}
		if !known || string(stored) != name {
			t.Fatalf("%s parses to %q (known %t)", name, stored, known)
		}
	}
	if _, known := parseSeriesStatus("SERIES_STATUS_ARCHIVED"); known {
		t.Fatal("unknown status parsed")
	}
}
