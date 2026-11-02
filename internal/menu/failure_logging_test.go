package menu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func captureMenuLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

type failingMenuPermissionChecker struct{ err error }

func (c failingMenuPermissionChecker) Can(context.Context, policyv1.AuthorizationDecision) (bool, error) {
	return false, c.err
}

// A permission failure is not logged here: the concealing client error keeps
// the cause for the one request.completed record.
func TestMenuPermissionFailureKeepsCauseForTheRequestRecord(t *testing.T) {
	principal := &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	}
	actionCause := errors.New("menu policy action unavailable")
	spiceDBCause := errors.New("spicedb connection refused")
	for _, tc := range []struct {
		name    string
		checker menuPermissionChecker
		action  menuAction
		code    connect.Code
		cause   error
	}{
		{
			name:    "action",
			checker: &recordingMenuPermissionChecker{allowed: true},
			action:  func(string) (policyv1.Can, error) { return policyv1.Can{}, actionCause },
			code:    connect.CodeNotFound,
			cause:   actionCause,
		},
		{
			name:    "spicedb",
			checker: failingMenuPermissionChecker{err: spiceDBCause},
			action:  policyv1.Menu.Edit,
			code:    connect.CodeUnavailable,
			cause:   spiceDBCause,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := captureMenuLogs(t)
			err := checkMenuPermission(t.Context(), tc.checker, uuid.NewString(), tc.action, principal)
			require.Equal(t, tc.code, errs.CodeOf(err))
			require.ErrorIs(t, err, tc.cause)
			require.Contains(t, failurecause.Message(err), tc.cause.Error())
			require.Empty(t, output.String())
		})
	}
}

func TestInvalidTranslationLabelDocumentKeepsCauseWithoutLogging(t *testing.T) {
	output := captureMenuLogs(t)
	_, err := DecodeTranslationLabelValues([]byte(`[{"id":"private-label",`))
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
	require.Equal(t, errs.InvalidArgument("content_json", "must be a valid Menu label document").Message(), errs.Wrap(err).Message())
	require.Contains(t, failurecause.Message(err), "unexpected end of JSON input")
	require.Empty(t, output.String())
}

func TestTopologyDecodeFailureLogsCauseBeforeReturningFalse(t *testing.T) {
	for _, tc := range []struct {
		name, stored, requested, level, operation string
	}{
		{"stored", "[", "[]", "ERROR", "compare_stored_topology"},
		{"requested", "[]", "[", "WARN", "compare_requested_topology"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
			t.Cleanup(func() { slog.SetDefault(previous) })

			require.False(t, menuItemTopologyEqual(t.Context(), "menu-topology-test", []byte(tc.stored), []byte(tc.requested)))
			var entry map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
			require.Equal(t, tc.level, entry["level"])
			require.Equal(t, tc.operation, entry["operation"])
			require.Equal(t, "unexpected end of JSON input", entry["failure_cause"])
			require.Equal(t, "menu-topology-test", entry["menu_id"])
		})
	}
}
