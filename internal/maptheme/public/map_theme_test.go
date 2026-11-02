package public

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/maptheme/mapthemetest"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestPublicMapThemeResolveByIDsRejectsMalformedIDBeforeDB(t *testing.T) {
	_, err := NewConnectHandler(&Service{}).ResolveByIds(t.Context(), connect.NewRequest(&openv1.ResolveMapThemesByIdsRequest{
		RequestedThemeIds: []string{"not-a-uuid"},
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestPublicMapThemeResolveByIDsOmitsOnlyExactEmptyIDs(t *testing.T) {
	response, err := NewConnectHandler(&Service{}).ResolveByIds(t.Context(), connect.NewRequest(&openv1.ResolveMapThemesByIdsRequest{
		RequestedThemeIds: []string{"", ""},
	}))
	require.NoError(t, err)
	require.Empty(t, response.Msg.Results)

	for _, value := range []string{" ", "\t", " 550e8400-e29b-41d4-a716-446655440000 "} {
		_, err := NewConnectHandler(&Service{}).ResolveByIds(t.Context(), connect.NewRequest(&openv1.ResolveMapThemesByIdsRequest{
			RequestedThemeIds: []string{"", value},
		}))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	}
}

func TestPublicMapThemeResolveRejectsWhitespaceWrappedOrWhitespaceOnlyIDBeforeDB(t *testing.T) {
	for _, value := range []string{" ", "\t", " 550e8400-e29b-41d4-a716-446655440000 "} {
		_, err := NewConnectHandler(&Service{}).Resolve(t.Context(), connect.NewRequest(&openv1.ResolveMapThemeRequest{
			ThemeId: &value,
			Scheme:  "light",
		}))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	}
}

func publicHandler(t *testing.T) (*ConnectHandler, *mapthemetest.Store) {
	t.Helper()
	store, units := mapthemetest.New()
	service, err := NewService(Deps{Units: units})
	require.NoError(t, err)
	return NewConnectHandler(service), store
}

func TestPublicMapThemeResolveByIDsPreservesOrderDuplicatesAndFallsBack(t *testing.T) {
	handler, store := publicHandler(t)
	other := store.Seed(mapthemetest.Snapshot("Other"))
	defaultID, unknown := store.Default(), uuid.NewString()
	requested := []string{defaultID, unknown, other.ID, "", defaultID}

	response, err := handler.ResolveByIds(t.Context(), connect.NewRequest(&openv1.ResolveMapThemesByIdsRequest{
		RequestedThemeIds: requested,
	}))

	require.NoError(t, err)
	var got [][2]string
	for _, result := range response.Msg.Results {
		got = append(got, [2]string{result.RequestedThemeId, result.Theme.Id})
		require.Equal(t, result.Theme.Id+":light", result.Theme.LightVariant.Id)
		require.Equal(t, result.Theme.Id+":dark", result.Theme.DarkVariant.Id)
	}
	require.Equal(t, [][2]string{{defaultID, defaultID}, {unknown, defaultID}, {other.ID, other.ID}, {defaultID, defaultID}}, got)
}

func TestPublicMapThemeUnknownResolveFallsBackForBothSchemes(t *testing.T) {
	handler, store := publicHandler(t)
	stored, _ := store.Stored(store.Default())
	unknown := uuid.NewString()
	for scheme, variant := range map[string]string{"light": stored.Snapshot.LightVariant.BackgroundColor, "dark": stored.Snapshot.DarkVariant.BackgroundColor} {
		response, err := handler.Resolve(t.Context(), connect.NewRequest(&openv1.ResolveMapThemeRequest{
			ThemeId: &unknown, Scheme: scheme,
		}))
		require.NoError(t, err)
		require.Equal(t, store.Default(), response.Msg.ThemeId)
		require.Equal(t, scheme, response.Msg.Scheme)
		require.Equal(t, store.Default()+":"+scheme, response.Msg.Variant.Id)
		require.Equal(t, variant, response.Msg.Variant.BackgroundColor)
	}
}

func TestPublicMapThemeResolveDefaultsToTheLightSchemeOfTheRequestedTheme(t *testing.T) {
	handler, store := publicHandler(t)
	snapshot := mapthemetest.Snapshot("Requested")
	snapshot.Settings.CalloutScale = 1.1
	requested := store.Seed(snapshot)

	response, err := handler.Resolve(t.Context(), connect.NewRequest(&openv1.ResolveMapThemeRequest{ThemeId: &requested.ID}))

	require.NoError(t, err)
	require.Equal(t, requested.ID, response.Msg.ThemeId)
	require.Equal(t, "light", response.Msg.Scheme)
	require.Equal(t, float64(float32(1.1)), response.Msg.Settings.CalloutScale)
	require.Equal(t, []string{"name", "address"}, response.Msg.Settings.CalloutFields)
}
