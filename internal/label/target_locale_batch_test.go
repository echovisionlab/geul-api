package label

import (
	"testing"

	tb "github.com/echovisionlab/geul-api/internal/testutil/targetbatchtest"
)

func TestValidateLabelTargetBatchOutcomes(t *testing.T) {
	identity := tb.Invalid("batch: Label target document identity and revision must match the locked state")
	tb.Run(t, validateLabelTargetBatch, []tb.Case{
		{Kind: tb.DocumentMismatch, Want: identity},
		{Kind: tb.RevisionMismatch, Want: identity},
		{Kind: tb.GraphUpsert, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Label target locale cannot mutate the shared Block graph")},
		{Kind: tb.GraphDelete, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Label target locale cannot mutate the shared Block graph")},
		{Kind: tb.GraphReorder, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Label target locale cannot mutate the shared Block graph")},
		{Kind: tb.MultipleLocaleGroups, Want: tb.Invalid("batch: Label target locale contains multiple locale groups")},
		{Kind: tb.LocaleMismatch, Want: tb.Rejected(tb.RoomLocaleMismatch, "Label target locale mutation must match the authenticated room locale")},
		{Kind: tb.LocaleDelete, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Label target locale units cannot be deleted by interactive editing; set an explicit empty value instead")},
		{Kind: tb.LocaleDeleteAllowed, Want: tb.Accepted()},
		{Kind: tb.LocaleUpsert, Want: tb.Accepted()},
		{Kind: tb.Empty, Want: tb.Accepted()},
	})
}
