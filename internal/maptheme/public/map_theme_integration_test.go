//go:build integration

package public

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPublicMapThemeResolveByIDsPreservesOrderDuplicatesAndFallsBack(t *testing.T) {
	db := newPublicIntegrationDB(t)
	var settings model.SiteSettings
	require.NoError(t, db.Select("default_map_theme_id").Where("id = ?", 1).Take(&settings).Error)
	require.NotEmpty(t, settings.DefaultMapThemeID)
	unknown := uuid.NewString()
	requested := []string{settings.DefaultMapThemeID, unknown, settings.DefaultMapThemeID}

	response, err := NewMapThemeService(db).ResolveByIds(t.Context(), connect.NewRequest(&openv1.ResolveMapThemesByIdsRequest{
		RequestedThemeIds: requested,
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.Results, 3)
	for index, result := range response.Msg.Results {
		require.Equal(t, requested[index], result.RequestedThemeId)
		require.Equal(t, settings.DefaultMapThemeID, result.Theme.Id)
		require.NotNil(t, result.Theme.LightVariant)
		require.NotNil(t, result.Theme.DarkVariant)
	}
}

func TestPublicMapThemeUnknownResolveFallsBackForBothSchemes(t *testing.T) {
	db := newPublicIntegrationDB(t)
	var settings model.SiteSettings
	require.NoError(t, db.Select("default_map_theme_id").Where("id = ?", 1).Take(&settings).Error)
	unknown := uuid.NewString()
	for _, scheme := range []string{"light", "dark"} {
		response, err := NewMapThemeService(db).Resolve(t.Context(), connect.NewRequest(&openv1.ResolveMapThemeRequest{
			ThemeId: &unknown, Scheme: scheme,
		}))
		require.NoError(t, err)
		require.Equal(t, settings.DefaultMapThemeID, response.Msg.ThemeId)
		require.Equal(t, scheme, response.Msg.Scheme)
		require.NotNil(t, response.Msg.Variant)
	}
}
