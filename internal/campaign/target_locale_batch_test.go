package campaign

import (
	"testing"

	tb "github.com/echovisionlab/geul-api/internal/testutil/targetbatchtest"
)

func TestValidateCampaignTargetBatchOutcomes(t *testing.T) {
	identity := tb.Invalid("batch: Campaign target document identity and revision must match the locked state")
	tb.Run(t, validateCampaignTargetBatch, []tb.Case{
		{Kind: tb.DocumentMismatch, Want: identity},
		{Kind: tb.RevisionMismatch, Want: identity},
		{Kind: tb.GraphUpsert, Want: tb.Invalid("batch: Campaign target locale cannot mutate the shared Block graph")},
		{Kind: tb.GraphDelete, Want: tb.Invalid("batch: Campaign target locale cannot mutate the shared Block graph")},
		{Kind: tb.GraphReorder, Want: tb.Invalid("batch: Campaign target locale cannot mutate the shared Block graph")},
		{Kind: tb.MultipleLocaleGroups, Want: tb.Invalid("batch: Campaign target locale contains multiple locale groups")},
		{Kind: tb.LocaleMismatch, Want: tb.Invalid("batch: Campaign target mutation must match the authorized locale")},
		{Kind: tb.LocaleDelete, Want: tb.Invalid("batch: Campaign target locale values use explicit empty and cannot be deleted")},
		{Kind: tb.LocaleDeleteAllowed, Want: tb.Accepted()},
		{Kind: tb.LocaleUpsert, Want: tb.Accepted()},
		{Kind: tb.Empty, Want: tb.Accepted()},
	})
}
