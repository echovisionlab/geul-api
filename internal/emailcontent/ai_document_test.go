package emailcontent

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

var testOwner = Owner[Store]{Name: "Campaign", Entity: "campaign", IDField: "campaign_id"}

func TestNormalizeAIDocumentMutationKeepsLocaleLifecycleExclusive(t *testing.T) {
	t.Parallel()
	base := AIDocumentMutation{
		Reference: uuid.NewString(), Locale: "ja", ExpectedSource: "en",
		ExpectedDocumentRevision: uuid.NewString(), ContributorMember: uuid.New(),
	}
	source := AIDocumentMutation{
		Reference: base.Reference, Locale: "en", ExpectedSource: "en",
		ExpectedDocumentRevision: base.ExpectedDocumentRevision, ContributorMember: base.ContributorMember,
	}
	tests := []struct {
		name  string
		input AIDocumentMutation
		valid bool
	}{
		{name: "implicit target create with values", input: withBatch(base), valid: true},
		{name: "create absent target", input: withCreate(base), valid: true},
		{name: "delete existing target", input: withDelete(base), valid: true},
		{name: "create existing target", input: func() AIDocumentMutation {
			value := withCreate(base)
			value.ExpectedPresence = true
			return value
		}()},
		{name: "create source", input: withCreate(source)},
		{name: "delete source", input: withDelete(source)},
		{name: "delete absent target", input: func() AIDocumentMutation {
			value := base
			value.DeleteTranslation = true
			return value
		}()},
		{name: "mixed modes", input: func() AIDocumentMutation {
			value := withBatch(base)
			value.CreateTranslation = true
			return value
		}()},
		{name: "missing mode", input: base},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := testOwner.normalizeAIDocumentMutation(test.input)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
		})
	}
}

func withBatch(input AIDocumentMutation) AIDocumentMutation {
	input.Batch = &contentblock.Batch{}
	return input
}

func withCreate(input AIDocumentMutation) AIDocumentMutation {
	input.CreateTranslation = true
	return input
}

func withDelete(input AIDocumentMutation) AIDocumentMutation {
	input.ExpectedPresence = true
	input.DeleteTranslation = true
	return input
}

func TestLocaleContentOperationDistinguishesSourceAndTargetCRUD(t *testing.T) {
	t.Parallel()
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, LocaleContentOperation(true, false, false, true))
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, LocaleContentOperation(false, true, false, false))
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, LocaleContentOperation(false, false, false, false))
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, LocaleContentOperation(false, false, false, true))
	require.Equal(t, sharedtelemetry.AuditItemOperationDeleted, LocaleContentOperation(false, false, true, true))
}
