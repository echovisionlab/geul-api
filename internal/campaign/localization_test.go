package campaign

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
)

func TestApplyCampaignLocalizedContentFallsBackOnlyForMissingTargetSubject(t *testing.T) {
	sourceSubject := "Source subject"
	localized := applyCampaignLocalizedContent(
		Campaign{Subject: "stale", ContentHTML: stringPointer("<p>stale</p>")},
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
		Campaign{},
		&localizedCampaignContentRow{Subject: &sourceSubject},
		&localizedCampaignContentRow{Subject: &emptyTargetSubject},
		contentblock.MaterializedContent{},
	)

	require.Empty(t, localized.Subject)
	require.NotNil(t, localized.ContentHTML)
	require.Empty(t, ptrStringValue(localized.ContentHTML))
}

func stringPointer(value string) *string { return &value }
