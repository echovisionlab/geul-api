package page

import (
	"testing"

	tb "github.com/echovisionlab/geul-api/internal/testutil/targetbatchtest"
)

func TestValidatePageTargetBatchAuthorityOutcomes(t *testing.T) {
	identity := tb.Invalid("batch: Page target document identity and revision must match the locked state")
	tb.Run(t, validatePageTargetBatchAuthority, []tb.Case{
		{Kind: tb.DocumentMismatch, Want: identity},
		{Kind: tb.RevisionMismatch, Want: identity},
		{Kind: tb.GraphUpsert, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Page target locale cannot mutate the shared section graph or File ownership")},
		{Kind: tb.GraphDelete, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Page target locale cannot mutate the shared section graph or File ownership")},
		{Kind: tb.GraphReorder, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Page target locale cannot mutate the shared section graph or File ownership")},
		{Kind: tb.MultipleLocaleGroups, Want: tb.Invalid("batch: Page target locale contains multiple locale groups")},
		{Kind: tb.LocaleMismatch, Want: tb.Rejected(tb.RoomLocaleMismatch, "Page target mutation must match the authorized locale")},
		{Kind: tb.LocaleDelete, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Page target patch cannot unset locale Blocks; write explicit empty values instead")},
		{Kind: tb.LocaleDeleteAllowed, Want: tb.Accepted()},
		{Kind: tb.LocaleUpsert, Want: tb.Accepted()},
		{Kind: tb.Empty, Want: tb.Accepted()},
	})
}
