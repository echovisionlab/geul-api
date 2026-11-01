package release

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Stored values equal the API enum names, so every API status except
// UNSPECIFIED must round-trip; a new API status fails here until it is mapped.
func TestReleaseStatusMappingCoversEveryAPIStatus(t *testing.T) {
	t.Parallel()
	for value, name := range managev1.ReleaseStatus_name {
		status := managev1.ReleaseStatus(value)
		stored, known := releaseStatusFromProto(status)
		if status == managev1.ReleaseStatus_RELEASE_STATUS_UNSPECIFIED {
			if known {
				t.Fatalf("UNSPECIFIED mapped to %q", stored)
			}
			continue
		}
		if !known || string(stored) != name || releaseStatusToProto(stored) != status {
			t.Fatalf("%s maps to %q (known %t) and back to %s", name, stored, known, releaseStatusToProto(stored))
		}
	}
	if got := releaseStatusToProto(Status("RELEASE_STATUS_ARCHIVED")); got != managev1.ReleaseStatus_RELEASE_STATUS_UNSPECIFIED {
		t.Fatalf("unknown stored status mapped to %s", got)
	}
}

// Every API type except UNSPECIFIED round-trips; UNSPECIFIED and unknown
// stored values fall back to an album as they always have.
func TestReleaseTypeMappingCoversEveryAPIType(t *testing.T) {
	t.Parallel()
	for value, name := range managev1.ReleaseType_name {
		releaseType := managev1.ReleaseType(value)
		stored := releaseTypeFromProto(releaseType)
		if releaseType == managev1.ReleaseType_RELEASE_TYPE_UNSPECIFIED {
			if stored != TypeAlbum {
				t.Fatalf("UNSPECIFIED stored as %q", stored)
			}
			continue
		}
		if string(stored) != name || releaseTypeToProto(stored) != releaseType {
			t.Fatalf("%s stored as %q and read back as %s", name, stored, releaseTypeToProto(stored))
		}
	}
	if got := releaseTypeToProto(Type("RELEASE_TYPE_MIXTAPE")); got != managev1.ReleaseType_RELEASE_TYPE_ALBUM {
		t.Fatalf("unknown stored type read as %s", got)
	}
}
