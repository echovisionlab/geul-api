package menu

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalizeItemLocalization(t *testing.T) {
	t.Run("keeps explicit translated mode", func(t *testing.T) {
		mode := LocalizationTranslated
		normalizedMode, fixedLocale := CanonicalizeItemLocalization(&mode, nil)
		require.NotNil(t, normalizedMode)
		require.Equal(t, LocalizationTranslated, *normalizedMode)
		require.Nil(t, fixedLocale)
	})

	t.Run("keeps explicit fixed locale mode", func(t *testing.T) {
		mode := LocalizationFixedLocale
		locale := "ko-KR"
		normalizedMode, fixedLocale := CanonicalizeItemLocalization(&mode, &locale)
		require.NotNil(t, normalizedMode)
		require.Equal(t, LocalizationFixedLocale, *normalizedMode)
		require.NotNil(t, fixedLocale)
		require.Equal(t, "ko", *fixedLocale)
	})

	t.Run("infers fixed locale mode from locale", func(t *testing.T) {
		locale := "ja-JP"
		normalizedMode, fixedLocale := CanonicalizeItemLocalization(nil, &locale)
		require.NotNil(t, normalizedMode)
		require.Equal(t, LocalizationFixedLocale, *normalizedMode)
		require.NotNil(t, fixedLocale)
		require.Equal(t, "ja", *fixedLocale)
	})

	t.Run("keeps default mode unspecified", func(t *testing.T) {
		normalizedMode, fixedLocale := CanonicalizeItemLocalization(nil, nil)
		require.Nil(t, normalizedMode)
		require.Nil(t, fixedLocale)
	})
}
