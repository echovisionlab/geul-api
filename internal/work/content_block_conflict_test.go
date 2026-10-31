package work

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/conflicttest"
)

func TestNormalizeWorkContentBlockErrorKeepsRevisionConflictCause(t *testing.T) {
	conflicttest.RequireStoreConflictsKeepCause(t, normalizeWorkContentBlockError)
}
