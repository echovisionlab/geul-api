package emailauthoring

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/google/uuid"
)

func TestValidateEmailTemplateAIDocumentMutationKeepsLocaleLifecycleExclusive(t *testing.T) {
	t.Parallel()
	base := AIDocumentMutation{
		TemplateID: uuid.NewString(), Locale: "ko", ExpectedSource: "en",
		ExpectedDocumentRevision: uuid.NewString(), ContributorMember: uuid.New(),
	}
	tests := []struct {
		name  string
		input AIDocumentMutation
		valid bool
	}{
		{name: "implicit target create with values", input: withEmailTemplateAIDocumentBatch(base), valid: true},
		{name: "create absent target", input: withEmailTemplateAIDocumentCreate(base), valid: true},
		{name: "delete existing target", input: withEmailTemplateAIDocumentDelete(base), valid: true},
		{name: "create source", input: withEmailTemplateAIDocumentCreate(AIDocumentMutation{
			TemplateID: base.TemplateID, Locale: "en", ExpectedSource: "en", ExpectedDocumentRevision: base.ExpectedDocumentRevision,
			ContributorMember: base.ContributorMember,
		})},
		{name: "delete absent target", input: func() AIDocumentMutation {
			value := base
			value.DeleteTranslation = true
			return value
		}()},
		{name: "mixed modes", input: func() AIDocumentMutation {
			value := withEmailTemplateAIDocumentBatch(base)
			value.CreateTranslation = true
			return value
		}()},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := validateEmailTemplateAIDocumentMutation(test.input)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
		})
	}
}

func withEmailTemplateAIDocumentBatch(input AIDocumentMutation) AIDocumentMutation {
	input.Batch = &contentblock.Batch{}
	return input
}

func withEmailTemplateAIDocumentCreate(input AIDocumentMutation) AIDocumentMutation {
	input.CreateTranslation = true
	return input
}

func withEmailTemplateAIDocumentDelete(input AIDocumentMutation) AIDocumentMutation {
	input.ExpectedPresence = true
	input.DeleteTranslation = true
	return input
}
