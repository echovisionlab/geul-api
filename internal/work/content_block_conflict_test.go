package work

import (
	"testing"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/testutil/conflicttest"
)

func TestNormalizeWorkContentBlockErrorKeepsRevisionConflictCause(t *testing.T) {
	conflicttest.RequireStoreConflictsKeepCause(t, func(err error) error {
		return errs.Wrap(normalizeWorkContentBlockError(err))
	})
}
