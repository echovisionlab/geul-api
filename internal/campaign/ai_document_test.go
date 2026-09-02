package campaign

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/google/uuid"
)

func TestValidateCampaignAIDocumentMutationKeepsLocaleLifecycleExclusive(t *testing.T) {
	t.Parallel()
	base := AIDocumentMutation{
		CampaignID: uuid.NewString(), Locale: "ja", ExpectedSource: "en",
		ExpectedDocumentRevision: uuid.NewString(), ContributorMember: uuid.New(),
	}
	tests := []struct {
		name  string
		input AIDocumentMutation
		valid bool
	}{
		{name: "implicit target create with values", input: withCampaignAIDocumentBatch(base), valid: true},
		{name: "create absent target", input: withCampaignAIDocumentCreate(base), valid: true},
		{name: "delete existing target", input: withCampaignAIDocumentDelete(base), valid: true},
		{name: "create existing target", input: func() AIDocumentMutation {
			value := withCampaignAIDocumentCreate(base)
			value.ExpectedPresence = true
			return value
		}()},
		{name: "delete source", input: withCampaignAIDocumentDelete(AIDocumentMutation{
			CampaignID: base.CampaignID, Locale: "en", ExpectedSource: "en", ExpectedDocumentRevision: base.ExpectedDocumentRevision,
			ContributorMember: base.ContributorMember,
		})},
		{name: "missing mode", input: base},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := validateCampaignAIDocumentMutation(test.input)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
		})
	}
}

func withCampaignAIDocumentBatch(input AIDocumentMutation) AIDocumentMutation {
	input.Batch = &contentblock.Batch{}
	return input
}

func withCampaignAIDocumentCreate(input AIDocumentMutation) AIDocumentMutation {
	input.CreateTranslation = true
	return input
}

func withCampaignAIDocumentDelete(input AIDocumentMutation) AIDocumentMutation {
	input.ExpectedPresence = true
	input.DeleteTranslation = true
	return input
}
