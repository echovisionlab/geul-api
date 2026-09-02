package application

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestTranslationRuntimeSettingsNormalizeProtectedTermsBeforeCompareAndProjection(t *testing.T) {
	settings, err := translationRuntimeSettingsFromProto(&managev1.TranslationSettings{
		DefaultLocale: "en", ProtectedTerms: []string{" Photoshop ", "Photoshop", "react native", "React Native", " "},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"Photoshop", "react native", "React Native"}, settings.ProtectedTerms)
	require.Equal(t, settings.ProtectedTerms, toProtoTranslationSettings(settings).ProtectedTerms)
	require.Empty(t, translationRuntimeSettingsChangedFields(settings, translationRuntimeSettings{
		DefaultLocale: "en", ProtectedTerms: []string{"Photoshop", "react native", "React Native"},
	}))
	require.Equal(t, []string{"protected_terms"}, translationRuntimeSettingsChangedFields(settings, translationRuntimeSettings{
		DefaultLocale: "en", ProtectedTerms: []string{"Photoshop", "React Native"},
	}))
}
