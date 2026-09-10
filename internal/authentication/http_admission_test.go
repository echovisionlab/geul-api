package authentication

import (
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
)

func TestAdmissionHeadersSurviveUnifiedProjection(t *testing.T) {
	header := http.Header{}
	(authCodeQuota{}).write(header)
	require.Empty(t, header)
	(authCodeQuota{limit: 10, remaining: 9, windowSeconds: 600}).write(header)
	header.Set("Retry-After", "60")
	header.Set("WWW-Authenticate", `Bearer realm="test"`)
	header.Set("X-Private", "secret")
	projected := canonicalUnifiedAuthResponseHeaders(header, []byte(`{}`))
	for _, name := range []string{"RateLimit", "RateLimit-Policy", "Retry-After", "WWW-Authenticate"} {
		require.Equal(t, header.Get(name), projected.Get(name))
	}
	require.Equal(t, `"auth-code-ip";r=9;t=600`, projected.Get("RateLimit"))
	require.Empty(t, projected.Get("X-Private"))
	require.Equal(t, "no-store", projected.Get("Cache-Control"))
}
