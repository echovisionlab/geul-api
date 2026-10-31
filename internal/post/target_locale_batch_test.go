package post

import (
	"testing"

	tb "github.com/echovisionlab/geul-api/internal/testutil/targetbatchtest"
)

func TestValidatePostTargetBatchOutcomes(t *testing.T) {
	identity := tb.Invalid("batch: Post target document identity and revision must match the locked state")
	tb.Run(t, validatePostTargetBatch, []tb.Case{
		{Kind: tb.DocumentMismatch, Want: identity},
		{Kind: tb.RevisionMismatch, Want: identity},
		{Kind: tb.GraphUpsert, Want: tb.Invalid("batch: Post target locale cannot mutate the shared Block graph")},
		{Kind: tb.GraphDelete, Want: tb.Invalid("batch: Post target locale cannot mutate the shared Block graph")},
		{Kind: tb.GraphReorder, Want: tb.Invalid("batch: Post target locale cannot mutate the shared Block graph")},
		{Kind: tb.MultipleLocaleGroups, Want: tb.Invalid("batch: Post target locale contains multiple locale groups")},
		{Kind: tb.LocaleMismatch, Want: tb.Invalid("batch: Post target locale mutation must match the authorized locale")},
		{Kind: tb.LocaleDelete, Want: tb.Invalid("batch: Post target locale values cannot be cleared; write an explicit empty value or delete the translation")},
		{Kind: tb.LocaleDeleteAllowed, Want: tb.Accepted()},
		{Kind: tb.LocaleUpsert, Want: tb.Accepted()},
		{Kind: tb.Empty, Want: tb.Accepted()},
	})
}
