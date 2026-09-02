package filemedia

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

func TestMaskPrivatePostAccessHidesDeniedResource(t *testing.T) {
	t.Parallel()

	denied := connect.NewError(connect.CodePermissionDenied, errors.New("denied"))
	masked := maskPrivatePostAccess(denied, "post-id")
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(masked))

	unauthenticated := connect.NewError(connect.CodeUnauthenticated, errors.New("unauthenticated"))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(maskPrivatePostAccess(unauthenticated, "post-id")))

	unavailable := connect.NewError(connect.CodeUnavailable, errors.New("unavailable"))
	require.Same(t, unavailable, maskPrivatePostAccess(unavailable, "post-id"))
}
