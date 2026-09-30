package collaboration

import (
	"testing"

	"connectrpc.com/connect"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/stretchr/testify/require"
)

func TestNormalizeCollaborationLocaleUsesLocaleNeutralMapThemeRoom(t *testing.T) {
	mapTheme := intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_MAP_THEME
	page := intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PAGE

	locale, err := normalizeCollaborationLocale(mapTheme, "und")
	require.NoError(t, err)
	require.Equal(t, "und", *locale)

	_, err = normalizeCollaborationLocale(mapTheme, "en")
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	locale, err = normalizeCollaborationLocale(page, "en")
	require.NoError(t, err)
	require.Equal(t, "en", *locale)

	_, err = normalizeCollaborationLocale(page, "und")
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = normalizeCollaborationLocale(page, "EN")
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
