package menu

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
)

func TestCanonicalizeItemLocalization(t *testing.T) {
	t.Run("keeps explicit translated mode", func(t *testing.T) {
		mode := model.MenuItemLocalizationModeTranslated
		normalizedMode, fixedLocale := CanonicalizeItemLocalization(&mode, nil)
		require.NotNil(t, normalizedMode)
		require.Equal(t, model.MenuItemLocalizationModeTranslated, *normalizedMode)
		require.Nil(t, fixedLocale)
	})

	t.Run("keeps explicit fixed locale mode", func(t *testing.T) {
		mode := model.MenuItemLocalizationModeFixedLocale
		locale := "ko-KR"
		normalizedMode, fixedLocale := CanonicalizeItemLocalization(&mode, &locale)
		require.NotNil(t, normalizedMode)
		require.Equal(t, model.MenuItemLocalizationModeFixedLocale, *normalizedMode)
		require.NotNil(t, fixedLocale)
		require.Equal(t, "ko", *fixedLocale)
	})

	t.Run("infers fixed locale mode from locale", func(t *testing.T) {
		locale := "ja-JP"
		normalizedMode, fixedLocale := CanonicalizeItemLocalization(nil, &locale)
		require.NotNil(t, normalizedMode)
		require.Equal(t, model.MenuItemLocalizationModeFixedLocale, *normalizedMode)
		require.NotNil(t, fixedLocale)
		require.Equal(t, "ja", *fixedLocale)
	})

	t.Run("keeps default mode unspecified", func(t *testing.T) {
		normalizedMode, fixedLocale := CanonicalizeItemLocalization(nil, nil)
		require.Nil(t, normalizedMode)
		require.Nil(t, fixedLocale)
	})
}
