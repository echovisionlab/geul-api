package page

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/failurecause"
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

func TestPermissionDependencyFailureKeepsCauseOnMappedError(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{IdentityID: "22222222-2222-4222-8222-222222222222", SessionID: "page-failure-logging-session", Authenticated: true})
	cause := errors.New("authorization transport unavailable: connection refused")
	err := requirePagePermission(ctx, &failedPagePermissionChecker{err: cause}, "11111111-1111-4111-8111-111111111111", policyv1.Page.View)
	require.Equal(t, connect.CodeUnavailable, errs.CodeOf(err))
	require.NotContains(t, errs.Wrap(err).Message(), "connection refused")
	// The request record logs the failure from what the error carries.
	require.Empty(t, output.String())
	require.ErrorIs(t, err, cause)
	failure := failurecause.Of(err)
	require.Contains(t, failure.Cause, cause.Error())
	require.Len(t, failure.Attrs, 1)
	require.Equal(t, "page_id", failure.Attrs[0].Key)
	require.Equal(t, "11111111-1111-4111-8111-111111111111", failure.Attrs[0].Value.String())
}

func TestContentFailureKeepsRedactedCauseOnMappedError(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	cause := fmt.Errorf("%w: render <img> using relation \"page_media\": password=hidden-pass user@example.com", contentblock.ErrFileReference)

	err := normalizePageContentBlockError(cause)
	require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))
	require.NotContains(t, errs.Wrap(err).Message(), "page_media")
	require.Empty(t, output.String())
	require.ErrorIs(t, err, cause)
	message := failurecause.Message(err)
	require.Contains(t, message, contentblock.ErrFileReference.Error()+`: render <img> using relation "page_media": password=[redacted] [redacted-email]`)
	require.NotContains(t, message, "hidden-pass")
	require.NotContains(t, message, "user@example.com")
}
