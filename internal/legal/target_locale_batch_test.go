package legal

import (
	"testing"

	tb "github.com/echovisionlab/geul-api/internal/testutil/targetbatchtest"
)

func TestValidateLegalTargetBatchOutcomes(t *testing.T) {
	identity := tb.Invalid("batch: legal target document identity and revision must match the locked state")
	tb.Run(t, validateLegalTargetBatch, []tb.Case{
		{Kind: tb.DocumentMismatch, Want: identity},
		{Kind: tb.RevisionMismatch, Want: identity},
		{Kind: tb.GraphUpsert, Want: tb.Rejected(tb.NonSourceStructureForbidden, "legal target locale cannot mutate the shared Block graph or File relations")},
		{Kind: tb.GraphDelete, Want: tb.Rejected(tb.NonSourceStructureForbidden, "legal target locale cannot mutate the shared Block graph or File relations")},
		{Kind: tb.GraphReorder, Want: tb.Rejected(tb.NonSourceStructureForbidden, "legal target locale cannot mutate the shared Block graph or File relations")},
		{Kind: tb.MultipleLocaleGroups, Want: tb.Rejected(tb.RoomLocaleMismatch, "legal target locale contains multiple locale groups")},
		{Kind: tb.LocaleMismatch, Want: tb.Rejected(tb.RoomLocaleMismatch, "legal target locale mutation must match the authorized room locale")},
		{Kind: tb.LocaleDelete, Want: tb.Rejected(tb.NonSourceStructureForbidden, "legal target locale values must be set explicitly; ordinary edits cannot unset or delete locale Blocks")},
		{Kind: tb.LocaleDeleteAllowed, Want: tb.Accepted()},
		{Kind: tb.LocaleUpsert, Want: tb.Accepted()},
		{Kind: tb.Empty, Want: tb.Accepted()},
	})
}
