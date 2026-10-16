package maptheme

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestNormalizeMapThemeColorRejectsValidLookingValueOverFiftyRunes(t *testing.T) {
	t.Parallel()
	value := "rgb(" + strings.Repeat(" ", 45) + "1,2,3)"
	_, ok := normalizeMapThemeColor(value)
	require.False(t, ok)
}

func TestNormalizeMapThemeNameUsesTrimmedUnicodeCodePoints(t *testing.T) {
	t.Parallel()
	accepted := strings.Repeat("🎧", 255)
	normalized, err := normalizeMapThemeName(" " + accepted + " ")
	require.NoError(t, err)
	require.Equal(t, accepted, normalized)

	_, err = normalizeMapThemeName(strings.Repeat("🎧", 256))
	require.Error(t, err)
}

func TestManageMapThemeResolveRejectsMalformedIDBeforeDB(t *testing.T) {
	t.Parallel()
	// Nil dependencies make any database or authorization access fail the test.
	malformed := "not-a-uuid"
	response, err := (&MapThemeService{}).ResolveMapTheme(context.Background(), connect.NewRequest(&managev1.ResolveMapThemeRequest{
		ThemeId: &malformed,
		Scheme:  "light",
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Nil(t, response)
}

func TestManageMapThemeResolveRejectsWhitespaceWrappedCanonicalIDBeforeDB(t *testing.T) {
	t.Parallel()
	wrapped := " 550e8400-e29b-41d4-a716-446655440000 "
	response, err := (&MapThemeService{}).ResolveMapTheme(context.Background(), connect.NewRequest(&managev1.ResolveMapThemeRequest{
		ThemeId: &wrapped,
		Scheme:  "light",
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Nil(t, response)
}

func TestNormalizeMapThemeIDRejectsWhitespaceInsteadOfTrimming(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		" ",
		"\t",
		" 550e8400-e29b-41d4-a716-446655440000 ",
		"550E8400-E29B-41D4-A716-446655440000",
	} {
		_, err := normalizeMapThemeID(value, "id")
		require.Error(t, err)
	}
}
