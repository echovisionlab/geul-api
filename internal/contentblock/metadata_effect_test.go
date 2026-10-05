package contentblock

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateMetadataEffectCommonInvariants(t *testing.T) {
	tests := []struct {
		name              string
		effect            MetadataEffect
		sourceLocale      string
		sourceLocaleLabel string
		wantError         string
	}{
		{
			name:              "no-op is valid",
			sourceLocale:      "en",
			sourceLocaleLabel: "result",
		},
		{
			name: "unchanged translation source is invalid",
			effect: MetadataEffect{
				AffectsTranslationSource: true,
			},
			sourceLocale:      "en",
			sourceLocaleLabel: "metadata",
			wantError:         "unchanged metadata cannot affect translation source",
		},
		{
			name: "invalid metadata source locale is rejected",
			effect: MetadataEffect{
				Changed:      true,
				SourceLocale: " ",
			},
			sourceLocale:      "en",
			sourceLocaleLabel: "metadata",
			wantError:         "metadata source locale",
		},
		{
			name: "source switch must affect translation source",
			effect: MetadataEffect{
				Changed:      true,
				SourceLocale: "ko",
			},
			sourceLocale:      "en",
			sourceLocaleLabel: "result",
			wantError:         "source locale change must affect translation source",
		},
		{
			name: "source switch may affect translation source",
			effect: MetadataEffect{
				Changed:                  true,
				AffectsTranslationSource: true,
				SourceLocale:             "ko",
			},
			sourceLocale:      "en",
			sourceLocaleLabel: "result",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMetadataEffect(tt.effect, tt.sourceLocale, tt.sourceLocaleLabel)
			if tt.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrInvalidMutation)
			require.ErrorContains(t, err, tt.wantError)
		})
	}
}

func TestBatchChangedLocalesKeepTheirAdditionalConstraint(t *testing.T) {
	effect := MetadataEffect{Changed: true, ChangedLocales: []string{"en", " en"}}
	require.NoError(t, validateMetadataEffect(effect, "en", "metadata"))
	require.ErrorIs(t, validateMetadataChangedLocales(effect.ChangedLocales), ErrInvalidMutation)
	require.NoError(t, validateMetadataChangedLocales([]string{"en", "ko"}))
}
