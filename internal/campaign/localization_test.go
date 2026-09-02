package campaign

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
)

func TestApplyCampaignLocalizedContentFallsBackOnlyForMissingTargetSubject(t *testing.T) {
	sourceSubject := "Source subject"
	localized := applyCampaignLocalizedContent(
		model.Campaign{Subject: "stale", ContentHTML: stringPointer("<p>stale</p>")},
		&localizedCampaignContentRow{Subject: &sourceSubject},
		&localizedCampaignContentRow{},
		contentblock.MaterializedContent{HTML: "<p>current target body</p>"},
	)

	require.Equal(t, "Source subject", localized.Subject)
	require.Equal(t, "<p>current target body</p>", ptrStringValue(localized.ContentHTML))
}

func TestApplyCampaignLocalizedContentPreservesExplicitEmptyTargetSubject(t *testing.T) {
	sourceSubject := "Source subject"
	emptyTargetSubject := ""
	localized := applyCampaignLocalizedContent(
		model.Campaign{},
		&localizedCampaignContentRow{Subject: &sourceSubject},
		&localizedCampaignContentRow{Subject: &emptyTargetSubject},
		contentblock.MaterializedContent{},
	)

	require.Empty(t, localized.Subject)
	require.NotNil(t, localized.ContentHTML)
	require.Empty(t, ptrStringValue(localized.ContentHTML))
}

func TestResolveLocalizedCampaignRequiresContentBlockStore(t *testing.T) {
	_, _, err := ResolveLocalizedCampaign(t.Context(), nil, nil, model.Campaign{}, "ko")
	require.ErrorContains(t, err, "Campaign content Block store")
}

func stringPointer(value string) *string { return &value }
