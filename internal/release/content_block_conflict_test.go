package release

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/conflicttest"
)

func TestNormalizeReleaseContentBlockErrorKeepsRevisionConflictCause(t *testing.T) {
	conflicttest.RequireStoreConflictsKeepCause(t, normalizeReleaseContentBlockError)
}
