package campaign

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/localization"
)

func TestCreateCampaignTargetDefinitionRequiresExplicitTarget(t *testing.T) {
	t.Parallel()

	criteriaID := uuid.NewString()
	empty, invalid := "", "not-a-uuid"
	tests := []struct {
		name         string
		target       CreateCampaignTarget
		wantMode     string
		wantCriteria *string
		wantErr      bool
	}{
		{name: "unset target", wantErr: true},
		{name: "empty all", target: CreateCampaignTarget{allMissing: true}, wantErr: true},
		{name: "all", target: CreateCampaignTarget{All: true}, wantMode: CampaignTargetModeAll},
		{name: "empty criteria", target: CreateCampaignTarget{CriteriaID: &empty}, wantErr: true},
		{name: "invalid criteria", target: CreateCampaignTarget{CriteriaID: &invalid}, wantErr: true},
		{name: "criteria", target: CreateCampaignTarget{CriteriaID: &criteriaID}, wantMode: CampaignTargetModeMemberCriteria, wantCriteria: &criteriaID},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode, criteria, err := createCampaignTargetDefinition(test.target)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantMode, mode)
			require.Equal(t, test.wantCriteria, criteria)
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
