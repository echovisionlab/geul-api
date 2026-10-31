package artist

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

type failedPermissionChecker struct{ cause error }

func (c failedPermissionChecker) Can(context.Context, policyv1.AuthorizationDecision) (bool, error) {
	return false, c.cause
}

// captureLogs routes the default logger into a buffer, so a test can assert
// that a failure is left to the request record instead of being logged here.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

func TestPermissionFailureKeepsRedactedCauseOnGenericDependencyError(t *testing.T) {
	output := captureLogs(t)
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID:    auth.IdentityID("11111111-1111-4111-8111-111111111111"),
		SessionID:     auth.SessionID("44444444-4444-4444-8444-444444444444"),
		Authenticated: true,
	})
	cause := fmt.Errorf("permission check: %w", errors.New(`relation "artist_participant" <view> unavailable: password=private-secret https://user:pass@auth.test/check?token=private-token private@example.test`))
	err := errs.Wrap(artistEntity.RequireCreate(ctx, failedPermissionChecker{cause}))
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.NotContains(t, err.Error(), cause.Error())
	require.ErrorIs(t, err, cause)
	require.Empty(t, output.String())

	message := failurecause.Message(err)
	require.Contains(t, message, `permission check: relation "artist_participant" <view> unavailable`)
	require.Contains(t, message, "https://[redacted]@auth.test/check?[redacted]")
	for _, private := range []string{"private-secret", "user:pass", "private-token", "private@example.test"} {
		require.NotContains(t, message, private)
	}
}

func TestContentBlockRejectionKeepsWrappedCauseOnStatusMapping(t *testing.T) {
	output := captureLogs(t)
	cause := fmt.Errorf(`element <file> in relation "content_block": password=private-secret: %w`, contentblock.ErrFileReference)
	err := errs.Wrap(normalizeCreativeContentBlockError(artistContentEntity, cause))
	require.Equal(t, connect.CodeInvalidArgument, err.Code())
	require.NotContains(t, err.Error(), cause.Error())
	require.ErrorIs(t, err, contentblock.ErrFileReference)
	require.Empty(t, output.String())

	message := failurecause.Message(err)
	require.Contains(t, message, `element <file> in relation "content_block"`)
	require.Contains(t, message, contentblock.ErrFileReference.Error())
	require.NotContains(t, message, "private-secret")
}

func TestContentBlockRevisionConflictDoesNotMatchRawStaleRevision(t *testing.T) {
	cause := &contentblock.StaleRevisionError{}
	err := normalizeCreativeContentBlockError(artistContentEntity, cause)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.NotErrorIs(t, err, contentblock.ErrStaleRevision)
}
