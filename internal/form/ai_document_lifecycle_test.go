package form

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestFormAIDocumentLifecycleUsesExactGeneratedStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status    string
		published bool
		valid     bool
	}{
		{status: managev1.FormStatus_FORM_STATUS_DRAFT.String(), valid: true},
		{status: managev1.FormStatus_FORM_STATUS_PUBLISHED.String(), published: true, valid: true},
		{status: "PUBLISHED"},
		{status: "FORM_STATUS_PUBLISHED "},
		{status: "BOGUS_PUBLISHED"},
		{status: "FORM_STATUS_ARCHIVED"},
		{status: managev1.FormStatus_FORM_STATUS_UNSPECIFIED.String()},
	}
	for _, test := range tests {
		test := test
		t.Run(test.status, func(t *testing.T) {
			t.Parallel()
			published, valid := formAIDocumentLifecycle(test.status)
			if published != test.published || valid != test.valid {
				t.Fatalf("formAIDocumentLifecycle(%q) = (%t, %t), want (%t, %t)", test.status, published, valid, test.published, test.valid)
			}
		})
	}
}
