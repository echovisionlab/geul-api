package public

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/stretchr/testify/require"
)

const publicNewsletterTokenTestSecret = "public-newsletter-token-test-secret-at-least-32-bytes"

// newTestService builds the public Member use cases through NewService.
// Unset collaborators get a SpiceDB client that is never dialed, the test
// token secret and a recording audit appender.
func newTestService(t *testing.T, deps Deps) *Service {
	t.Helper()
	if deps.SpiceDB == nil {
		deps.SpiceDB = &auth.SpiceDBClient{}
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	if deps.TokenSecret == "" {
		deps.TokenSecret = publicNewsletterTokenTestSecret
	}
	service, err := NewService(deps)
	require.NoError(t, err)
	return service
}
