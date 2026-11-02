package public

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/release"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// Stored values equal the public enum names, so every public status and
// type except UNSPECIFIED is the projection of the stored value of its
// name; a new API value fails here until it is mapped.
func TestPublicReleaseStatusAndTypeMappingsCoverEveryAPIValue(t *testing.T) {
	t.Parallel()
	for value, name := range openv1.ReleaseStatus_name {
		status := openv1.ReleaseStatus(value)
		if status == openv1.ReleaseStatus_RELEASE_STATUS_UNSPECIFIED {
			continue
		}
		if got := releaseStatusToOpen(release.Status(name)); got != status {
			t.Fatalf("stored %s projects as %s", name, got)
		}
	}
	for value, name := range openv1.ReleaseType_name {
		releaseType := openv1.ReleaseType(value)
		if releaseType == openv1.ReleaseType_RELEASE_TYPE_UNSPECIFIED {
			continue
		}
		if got := releaseTypeToOpen(release.Type(name)); got != releaseType {
			t.Fatalf("stored %s projects as %s", name, got)
		}
	}
	if releaseStatusToOpen("RELEASE_STATUS_ARCHIVED") != openv1.ReleaseStatus_RELEASE_STATUS_UNSPECIFIED ||
		releaseTypeToOpen("RELEASE_TYPE_MIXTAPE") != openv1.ReleaseType_RELEASE_TYPE_UNSPECIFIED {
		t.Fatal("an unknown stored value must project as UNSPECIFIED")
	}
}
