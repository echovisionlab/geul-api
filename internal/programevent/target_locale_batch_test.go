package programevent

import (
	"testing"

	tb "github.com/echovisionlab/geul-api/internal/testutil/targetbatchtest"
)

func TestValidateProgramEventTargetBatchOutcomes(t *testing.T) {
	identity := tb.Invalid("batch: Program Event target document identity and revision must match the locked state")
	graph := tb.Rejected(tb.NonSourceStructureForbidden, "Program Event target locale cannot mutate the shared Block graph or File ownership")
	tb.Run(t, tb.Shared(programEventTargetEntity), []tb.Case{
		{Kind: tb.DocumentMismatch, Want: identity},
		{Kind: tb.RevisionMismatch, Want: identity},
		{Kind: tb.GraphUpsert, Want: graph},
		{Kind: tb.GraphDelete, Want: graph},
		{Kind: tb.GraphReorder, Want: graph},
		{Kind: tb.MultipleLocaleGroups, Want: tb.Rejected(tb.RoomLocaleMismatch, "Program Event target locale contains multiple locale groups")},
		{Kind: tb.LocaleMismatch, Want: tb.Rejected(tb.RoomLocaleMismatch, "Program Event target mutation must match the authorized locale")},
		{Kind: tb.LocaleDelete, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Program Event target patch cannot unset locale Blocks; write explicit empty values instead")},
		{Kind: tb.LocaleDeleteAllowed, Want: tb.Accepted()},
		{Kind: tb.LocaleUpsert, Want: tb.Accepted()},
		{Kind: tb.Empty, Want: tb.Accepted()},
	})
}
