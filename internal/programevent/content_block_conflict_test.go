package programevent

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/conflicttest"
)

func TestNormalizeProgramEventContentBlockErrorKeepsRevisionConflictCause(t *testing.T) {
	conflicttest.RequireStoreConflictsKeepCause(t, normalizeProgramEventContentBlockError)
}
