package artist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

type failedPermissionChecker struct{ cause error }

func (c failedPermissionChecker) Can(context.Context, policyv1.AuthorizationDecision) (bool, error) {
	return false, c.cause
}

func TestPermissionFailureLogsCauseBeforeGenericDependencyError(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID:    auth.IdentityID("11111111-1111-4111-8111-111111111111"),
		SessionID:     auth.SessionID("44444444-4444-4444-8444-444444444444"),
		Authenticated: true,
	})
	cause := fmt.Errorf("permission check: %w", errors.New(`relation "artist_participant" <view> unavailable: password=private-secret https://user:pass@auth.test/check?token=private-token private@example.test`))
	ctx = sharedtelemetry.WithRequestContext(ctx, sharedtelemetry.RequestContext{RequestID: "request-1"})
	err := requireArtistCreate(ctx, failedPermissionChecker{cause})
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.NotContains(t, err.Error(), cause.Error())
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "ERROR", entry["level"])
	require.Contains(t, entry["failure_cause"], `permission check: relation "artist_participant" <view> unavailable`)
	require.Contains(t, entry["failure_cause"], "https://[redacted]@auth.test/check?[redacted]")
	for _, private := range []string{"private-secret", "user:pass", "private-token", "private@example.test"} {
		require.NotContains(t, output.String(), private)
	}
	require.Equal(t, "request-1", entry["request_id"])
	require.Equal(t, "artist", entry["domain"])
	require.Equal(t, "requireArtistGlobalAction", entry["operation"])
}

func TestContentBlockRejectionLogsWrappedCauseBeforeStatusMapping(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	cause := fmt.Errorf(`element <file> in relation "content_block": password=private-secret: %w`, contentblock.ErrFileReference)
	err := normalizeCreativeContentBlockError(artistContentEntity, cause)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.NotContains(t, err.Error(), cause.Error())
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "WARN", entry["level"])
	require.Contains(t, entry["failure_cause"], `element <file> in relation "content_block"`)
	require.Contains(t, entry["failure_cause"], contentblock.ErrFileReference.Error())
	require.NotContains(t, output.String(), "private-secret")
}
