package campaign

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/stretchr/testify/require"
)

type campaignLocaleNormalizerFunc func(string) *string

func (f campaignLocaleNormalizerFunc) NormalizeSupportedLocale(value string) *string {
	return f(value)
}

func TestResolveCampaignRequestedLocaleUsesExplicitLocaleFirst(t *testing.T) {
	explicit := "pt"

	locale, err := resolveCampaignRequestedLocale(campaignLocaleNormalizerFunc(localization.NormalizeSupportedLocale), &explicit)
	require.NoError(t, err)
	require.Equal(t, "pt-BR", locale)
}

func TestResolveCampaignRequestedLocaleDefaultsToSourceLocaleResolution(t *testing.T) {
	locale, err := resolveCampaignRequestedLocale(campaignLocaleNormalizerFunc(localization.NormalizeSupportedLocale), nil)
	require.NoError(t, err)
	require.Empty(t, locale)
}

func TestResolveCampaignRequestedLocaleRejectsUnsupportedExplicitLocale(t *testing.T) {
	explicit := "xx"

	locale, err := resolveCampaignRequestedLocale(campaignLocaleNormalizerFunc(localization.NormalizeSupportedLocale), &explicit)
	require.Empty(t, locale)
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
