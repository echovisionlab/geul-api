package public

import (
	"testing"

	"connectrpc.com/connect"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/stretchr/testify/require"
)

func TestPublicMapThemeResolveByIDsRejectsMalformedIDBeforeDB(t *testing.T) {
	_, err := NewMapThemeService(nil).ResolveByIds(t.Context(), connect.NewRequest(&openv1.ResolveMapThemesByIdsRequest{
		RequestedThemeIds: []string{"not-a-uuid"},
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestPublicMapThemeResolveByIDsOmitsOnlyExactEmptyIDs(t *testing.T) {
	response, err := NewMapThemeService(nil).ResolveByIds(t.Context(), connect.NewRequest(&openv1.ResolveMapThemesByIdsRequest{
		RequestedThemeIds: []string{"", ""},
	}))
	require.NoError(t, err)
	require.Empty(t, response.Msg.Results)

	for _, value := range []string{" ", "\t", " 550e8400-e29b-41d4-a716-446655440000 "} {
		_, err := NewMapThemeService(nil).ResolveByIds(t.Context(), connect.NewRequest(&openv1.ResolveMapThemesByIdsRequest{
			RequestedThemeIds: []string{"", value},
		}))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	}
}

func TestPublicMapThemeResolveRejectsWhitespaceWrappedOrWhitespaceOnlyIDBeforeDB(t *testing.T) {
	for _, value := range []string{" ", "\t", " 550e8400-e29b-41d4-a716-446655440000 "} {
		_, err := NewMapThemeService(nil).Resolve(t.Context(), connect.NewRequest(&openv1.ResolveMapThemeRequest{
			ThemeId: &value,
			Scheme:  "light",
		}))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	}
}
