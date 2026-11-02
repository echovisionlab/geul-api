package campaign

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/localization"
)

func TestCreateCampaignTargetDefinitionRequiresExplicitTarget(t *testing.T) {
	t.Parallel()

	segmentID := uuid.NewString()
	empty, invalid := "", "not-a-uuid"
	tests := []struct {
		name        string
		target      CreateCampaignTarget
		wantMode    string
		wantSegment *string
		wantErr     bool
	}{
		{name: "unset target", wantErr: true},
		{name: "empty all", target: CreateCampaignTarget{allMissing: true}, wantErr: true},
		{name: "all", target: CreateCampaignTarget{All: true}, wantMode: CampaignTargetModeAll},
		{name: "empty segment", target: CreateCampaignTarget{SegmentID: &empty}, wantErr: true},
		{name: "invalid segment", target: CreateCampaignTarget{SegmentID: &invalid}, wantErr: true},
		{name: "segment", target: CreateCampaignTarget{SegmentID: &segmentID}, wantMode: CampaignTargetModeSegment, wantSegment: &segmentID},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode, segment, err := createCampaignTargetDefinition(test.target)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantMode, mode)
			require.Equal(t, test.wantSegment, segment)
		})
	}
}

func TestCreateCampaignSourceLocaleIsExplicitAndCanonical(t *testing.T) {
	t.Parallel()

	normalizer := campaignLocaleNormalizerFunc(localization.NormalizeSupportedLocale)
	locale, err := normalizeCampaignCreateSourceLocale(normalizer, "pt")
	require.NoError(t, err)
	require.Equal(t, "pt-BR", locale)

	_, err = normalizeCampaignCreateSourceLocale(normalizer, "")
	require.Error(t, err)

	_, err = normalizeCampaignCreateSourceLocale(normalizer, "xx")
	require.Error(t, err)
}
