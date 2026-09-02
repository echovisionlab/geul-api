package aidocumentadapter

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewCampaignRegistrationRejectsMissingOwner(t *testing.T) {
	t.Parallel()
	if _, err := NewCampaignRegistration(nil); err == nil {
		t.Fatal("missing Campaign owner was accepted")
	}
}

func TestCampaignMutationMapsTheSharedEmailProfileCommand(t *testing.T) {
	t.Parallel()
	contributor := uuid.New()
	input := emailRichTextMutation{
		Reference: uuid.NewString(), Locale: "fr", ExpectedDocumentRevision: uuid.NewString(),
		ExpectedSource: "en", ExpectedPresence: false, ContributorMemberID: contributor,
		CreateTranslation: true,
	}
	mapped := campaignMutation(input)
	if mapped.CampaignID != input.Reference || mapped.Locale != input.Locale ||
		mapped.ContributorMember != contributor || !mapped.CreateTranslation {
		t.Fatalf("Campaign mutation mapping = %+v", mapped)
	}
}
