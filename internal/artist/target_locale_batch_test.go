package artist

import (
	"testing"

	tb "github.com/echovisionlab/geul-api/internal/testutil/targetbatchtest"
)

func TestValidateArtistTargetBatchAuthorityOutcomes(t *testing.T) {
	identity := tb.Invalid("batch: Artist target document identity and revision must match the locked state")
	tb.Run(t, validateArtistTargetBatchAuthority, []tb.Case{
		{Kind: tb.DocumentMismatch, Want: identity},
		{Kind: tb.RevisionMismatch, Want: identity},
		{Kind: tb.GraphUpsert, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Artist target locale cannot mutate the shared Block graph")},
		{Kind: tb.GraphDelete, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Artist target locale cannot mutate the shared Block graph")},
		{Kind: tb.GraphReorder, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Artist target locale cannot mutate the shared Block graph")},
		{Kind: tb.MultipleLocaleGroups, Want: tb.Invalid("batch: Artist target locale contains multiple locale groups")},
		{Kind: tb.LocaleMismatch, Want: tb.Rejected(tb.RoomLocaleMismatch, "Artist target mutation must match the authorized locale")},
		{Kind: tb.LocaleDelete, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Artist target patch cannot unset locale Blocks; write explicit empty values instead")},
		{Kind: tb.LocaleDeleteAllowed, Want: tb.Accepted()},
		{Kind: tb.LocaleUpsert, Want: tb.Accepted()},
		{Kind: tb.Empty, Want: tb.Accepted()},
	})
}
