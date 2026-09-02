//go:build integration

package maptheme

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func adminCtxForUser(user *testutil.OryUser) context.Context {
	return auth.WithUser(context.Background(), user.AuthUserInfo())
}

func TestNormalizeMapThemeColorRejectsValidLookingValueOverFiftyRunes(t *testing.T) {
	value := "rgb(" + strings.Repeat(" ", 45) + "1,2,3)"
	_, ok := normalizeMapThemeColor(value)
	require.False(t, ok)
}

func TestNormalizeMapThemeNameUsesTrimmedUnicodeCodePoints(t *testing.T) {
	accepted := strings.Repeat("🎧", 255)
	normalized, err := normalizeMapThemeName(" " + accepted + " ")
	require.NoError(t, err)
	require.Equal(t, accepted, normalized)

	_, err = normalizeMapThemeName(strings.Repeat("🎧", 256))
	require.Error(t, err)
}

func TestManageMapThemeResolveRejectsMalformedIDBeforeDB(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	malformed := "not-a-uuid"
	_, err := mapThemeServiceForTest(t, stack.DB, stack.SpiceDBClient).ResolveMapTheme(adminCtxForUser(admin), connect.NewRequest(&managev1.ResolveMapThemeRequest{
		ThemeId: &malformed,
		Scheme:  "light",
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestManageMapThemeResolveRejectsWhitespaceWrappedCanonicalIDBeforeDB(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	wrapped := " 550e8400-e29b-41d4-a716-446655440000 "
	_, err := mapThemeServiceForTest(t, stack.DB, stack.SpiceDBClient).ResolveMapTheme(adminCtxForUser(admin), connect.NewRequest(&managev1.ResolveMapThemeRequest{
		ThemeId: &wrapped,
		Scheme:  "light",
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestNormalizeMapThemeIDRejectsWhitespaceInsteadOfTrimming(t *testing.T) {
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
