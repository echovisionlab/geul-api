package programevent

import (
	"testing"

	tb "github.com/echovisionlab/geul-api/internal/testutil/targetbatchtest"
)

func TestValidateProgramEventTargetBatchOutcomes(t *testing.T) {
	identity := tb.Invalid("batch: Program Event target document identity and revision must match the locked state")
	tb.Run(t, validateProgramEventTargetBatch, []tb.Case{
		{Kind: tb.DocumentMismatch, Want: identity},
		{Kind: tb.RevisionMismatch, Want: identity},
		{Kind: tb.GraphUpsert, Want: tb.Invalid("batch: Program Event target locale cannot mutate the shared Block graph")},
		{Kind: tb.GraphDelete, Want: tb.Invalid("batch: Program Event target locale cannot mutate the shared Block graph")},
		{Kind: tb.GraphReorder, Want: tb.Invalid("batch: Program Event target locale cannot mutate the shared Block graph")},
		{Kind: tb.MultipleLocaleGroups, Want: tb.Invalid("batch: Program Event target locale contains multiple locale groups")},
		{Kind: tb.LocaleMismatch, Want: tb.Invalid("batch: Program Event target mutation must match the authorized locale")},
		{Kind: tb.LocaleDelete, Want: tb.Invalid("batch: Program Event target locale values use explicit empty and cannot be deleted")},
		{Kind: tb.LocaleDeleteAllowed, Want: tb.Accepted()},
		{Kind: tb.LocaleUpsert, Want: tb.Accepted()},
		{Kind: tb.Empty, Want: tb.Accepted()},
	})
}
