package page

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

type failedPagePermissionChecker struct {
	pageAuthorizationRecordingChecker
	err error
}

func (c *failedPagePermissionChecker) Can(context.Context, policyv1.AuthorizationDecision) (bool, error) {
	return false, c.err
}

func TestPermissionDependencyFailureLogsCauseBeforeMapping(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{IdentityID: "22222222-2222-4222-8222-222222222222", SessionID: "page-failure-logging-session", Authenticated: true})
	cause := errors.New("authorization transport unavailable: connection refused")
	err := requirePagePermission(ctx, &failedPagePermissionChecker{err: cause}, "11111111-1111-4111-8111-111111111111", policyv1.Page.View)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.NotContains(t, err.Error(), "connection refused")
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "ERROR", entry["level"])
	require.Equal(t, cause.Error(), entry["failure_cause"])
	require.Equal(t, "page.requirePagePermission", entry["operation"])
	require.Equal(t, "11111111-1111-4111-8111-111111111111", entry["page_id"])
}

func TestContentFailureLogsDiagnosticCauseBeforeMapping(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	cause := fmt.Errorf("%w: render <img> using relation \"page_media\": password=hidden-pass user@example.com", contentblock.ErrFileReference)

	err := normalizePageContentBlockError(cause)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.NotContains(t, err.Error(), "page_media")
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "WARN", entry["level"])
	require.Equal(t, contentblock.ErrFileReference.Error()+`: render <img> using relation "page_media": password=[redacted] [redacted-email]`, entry["failure_cause"])
	require.NotContains(t, output.String(), "hidden-pass")
	require.NotContains(t, output.String(), "user@example.com")
}
