package aidocumentadapter

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewEmailTemplateRegistrationRejectsMissingOwner(t *testing.T) {
	t.Parallel()
	if _, err := NewEmailTemplateRegistration(nil); err == nil {
		t.Fatal("missing Email Template owner was accepted")
	}
}

func TestEmailTemplateMutationMapsTheSharedEmailProfileCommand(t *testing.T) {
	t.Parallel()
	contributor := uuid.New()
	input := emailRichTextMutation{
		Reference: uuid.NewString(), Locale: "ko", ExpectedDocumentRevision: uuid.NewString(),
		ExpectedSource: "en", ExpectedPresence: true, ContributorMemberID: contributor,
		SetSubject: true, Subject: "", DeleteTranslation: false,
	}
	mapped := emailTemplateMutation(input)
	if mapped.TemplateID != input.Reference || mapped.Locale != input.Locale ||
		mapped.ContributorMember != contributor || !mapped.SetSubject || mapped.Subject != "" {
		t.Fatalf("Email Template mutation mapping = %+v", mapped)
	}
}
