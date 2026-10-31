package release

import (
	"testing"

	tb "github.com/echovisionlab/geul-api/internal/testutil/targetbatchtest"
)

func TestValidateReleaseTargetBatchOutcomes(t *testing.T) {
	identity := tb.Invalid("batch: Release target document identity and revision must match the locked state")
	tb.Run(t, validateReleaseTargetBatch, []tb.Case{
		{Kind: tb.DocumentMismatch, Want: identity},
		{Kind: tb.RevisionMismatch, Want: identity},
		{Kind: tb.GraphUpsert, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Release target locale cannot mutate the shared Block graph")},
		{Kind: tb.GraphDelete, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Release target locale cannot mutate the shared Block graph")},
		{Kind: tb.GraphReorder, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Release target locale cannot mutate the shared Block graph")},
		{Kind: tb.MultipleLocaleGroups, Want: tb.Invalid("batch: Release target locale contains multiple locale groups")},
		{Kind: tb.LocaleMismatch, Want: tb.Rejected(tb.RoomLocaleMismatch, "Release target mutation must match the authorized locale")},
		{Kind: tb.LocaleDelete, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Release target locale units cannot be deleted by interactive editing; write an explicit empty value instead")},
		{Kind: tb.LocaleDeleteAllowed, Want: tb.Accepted()},
		{Kind: tb.LocaleUpsert, Want: tb.Accepted()},
		{Kind: tb.Empty, Want: tb.Accepted()},
	})
}
