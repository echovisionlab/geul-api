package maptheme_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/maptheme/mapthemetest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestListMapThemesNeedsNoAuthorityAndOrdersByName(t *testing.T) {
	f := newFixture(t, &authztest.Checker{}, nil)
	later := f.seed("Zeta")
	earlier := f.seed("Alpha")

	list, err := f.themes.ListMapThemes(newPrincipal(t).ctx, connect.NewRequest(&managev1.ListMapThemesRequest{}))

	require.NoError(t, err)
	require.Equal(t, f.store.Default(), list.Msg.DefaultMapThemeId)
	var ids []string
	for _, theme := range list.Msg.Themes {
		ids = append(ids, theme.Id)
	}
	require.Equal(t, []string{earlier.ID, f.store.Default(), later.ID}, ids)
	require.Empty(t, f.checker.Checks())
}

func TestResolveMapThemeNeedsNoAuthorityAndFallsBackToTheDefaultForBothSchemes(t *testing.T) {
	f := newFixture(t, &authztest.Checker{}, nil)
	stored, _ := f.store.Stored(f.store.Default())
	ctx := newPrincipal(t).ctx
	unknown := uuid.NewString()

	for _, test := range []struct {
		themeID    *string
		scheme     string
		wantScheme string
		background string
	}{
		{nil, "dark", "dark", stored.Snapshot.DarkVariant.BackgroundColor},
		{&unknown, "light", "light", stored.Snapshot.LightVariant.BackgroundColor},
		{&unknown, "dark", "dark", stored.Snapshot.DarkVariant.BackgroundColor},
		{&unknown, "", "light", stored.Snapshot.LightVariant.BackgroundColor},
	} {
		resolved, err := f.themes.ResolveMapTheme(ctx, connect.NewRequest(&managev1.ResolveMapThemeRequest{ThemeId: test.themeID, Scheme: test.scheme}))
		require.NoError(t, err)
		require.Equal(t, f.store.Default(), resolved.Msg.ThemeId)
		require.Equal(t, test.wantScheme, resolved.Msg.Scheme)
		require.Equal(t, f.store.Default()+":"+test.wantScheme, resolved.Msg.Variant.Id)
		require.Equal(t, test.background, resolved.Msg.Variant.BackgroundColor)
	}
	require.Empty(t, f.checker.Checks())
}

func TestResolveMapThemeResolvesTheRequestedTheme(t *testing.T) {
	f := newFixture(t, nil, nil)
	snapshot := mapthemetest.Snapshot("Requested")
	snapshot.Settings.CalloutScale = 1.1
	requested := f.store.Seed(snapshot)

	resolved, err := f.themes.ResolveMapTheme(newPrincipal(t).ctx, connect.NewRequest(&managev1.ResolveMapThemeRequest{ThemeId: &requested.ID, Scheme: "dark"}))

	require.NoError(t, err)
	require.Equal(t, requested.ID, resolved.Msg.ThemeId)
	require.Equal(t, float64(float32(1.1)), resolved.Msg.Settings.CalloutScale)
	require.Equal(t, snapshot.DarkVariant.BackgroundColor, resolved.Msg.Variant.BackgroundColor)
}

func TestResolveMapThemeRejectsAnUnknownScheme(t *testing.T) {
	f := newFixture(t, nil, nil)
	_, err := f.themes.ResolveMapTheme(newPrincipal(t).ctx, connect.NewRequest(&managev1.ResolveMapThemeRequest{Scheme: "auto"}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestGetMapThemeAnswersTheStoredThemeToAnAdministrator(t *testing.T) {
	f := newFixture(t, nil, nil)
	snapshot := mapthemetest.Snapshot("Detail")
	snapshot.Settings.CalloutScale = 1.1
	snapshot.Settings.ShowAreaLabels = false
	theme := f.store.Seed(snapshot)
	admin := newPrincipal(t)

	got, err := f.themes.GetMapTheme(admin.ctx, connect.NewRequest(&managev1.GetMapThemeRequest{Id: theme.ID}))

	require.NoError(t, err)
	require.Equal(t, theme.ID, got.Msg.Id)
	require.Equal(t, "Detail", got.Msg.Name)
	require.EqualValues(t, 1, got.Msg.Revision)
	require.Equal(t, timestamppb.New(theme.CreatedAt).AsTime(), got.Msg.CreatedAt.AsTime())
	require.Equal(t, float64(float32(1.1)), got.Msg.Settings.CalloutScale)
	require.False(t, got.Msg.Settings.ShowAreaLabels)
	require.Equal(t, theme.ID+":light", got.Msg.LightVariant.Id)
	require.Equal(t, theme.ID+":dark", got.Msg.DarkVariant.Id)
	require.Equal(t, snapshot.DarkVariant.BackgroundColor, got.Msg.DarkVariant.BackgroundColor)
	require.Equal(t, []authztest.Check{checkOf(admin.identityID)(policyv1.MapTheme.View(theme.ID))}, f.checker.Checks())
}

func TestGetMapThemeRequiresAdministratorViewBeforeReading(t *testing.T) {
	f := newFixture(t, &authztest.Checker{}, nil)
	theme := f.seed("Hidden")

	_, err := f.themes.GetMapTheme(newPrincipal(t).ctx, connect.NewRequest(&managev1.GetMapThemeRequest{Id: theme.ID}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.ErrorContains(t, err, "admin access required")

	// A denied caller learns nothing about whether the theme exists.
	_, err = f.themes.GetMapTheme(newPrincipal(t).ctx, connect.NewRequest(&managev1.GetMapThemeRequest{Id: uuid.NewString()}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestGetMapThemeRejectsABannedPrincipalWithoutAsking(t *testing.T) {
	f := newFixture(t, nil, nil)
	theme := f.seed("Banned")
	banned := newPrincipal(t)
	user := *auth.GetUser(banned.ctx)
	user.Banned = true

	_, err := f.themes.GetMapTheme(auth.WithUser(banned.ctx, &user), connect.NewRequest(&managev1.GetMapThemeRequest{Id: theme.ID}))

	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Empty(t, f.checker.Checks())
}

func TestGetMapThemeAnswersAMissingThemeNotFound(t *testing.T) {
	f := newFixture(t, nil, nil)
	_, err := f.themes.GetMapTheme(newPrincipal(t).ctx, connect.NewRequest(&managev1.GetMapThemeRequest{Id: uuid.NewString()}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
