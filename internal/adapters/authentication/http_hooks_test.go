package authentication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/stretchr/testify/require"
)

const (
	hookIdentityID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	hookMemberID   = "bbbbbbbb-bbbb-4bbb-9bbb-bbbbbbbbbbbb"
)

func TestAfterLoginOnlyTranslatesHTTPAndApplicationResult(t *testing.T) {
	lifecycle := &recordingLoginHookLifecycle{result: authentication.LoginHookResult{
		MemberID: hookMemberID,
	}}
	handler := &HooksHandler{loginHooks: lifecycle}
	body, err := json.Marshal(AfterLoginRequest{
		IdentityID: hookIdentityID, Email: "member@example.test",
		PreferredLocale: "ko", Trigger: "registration",
	})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/hooks/after-login", bytes.NewReader(body))
	response := httptest.NewRecorder()

	handler.AfterLogin(response, request)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, []authentication.LoginHookInput{{
		IdentityID: hookIdentityID, Email: "member@example.test",
		PreferredLocale: "ko", Trigger: "registration",
	}}, lifecycle.inputs)
}

func TestNewHooksHandlerRequiresAuthenticationPorts(t *testing.T) {
	require.Panics(t, func() { NewHooksHandler(nil, &hookRegistrationPolicy{}) })
	require.Panics(t, func() { NewHooksHandler(&recordingLoginHookLifecycle{}, nil) })
}

func TestRejectCredentialRegistration(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		pendingEmail string
		wantStatus   int
		wantReason   string
	}{
		{name: "email code registration is allowed", method: "code", wantStatus: http.StatusOK},
		{name: "OIDC registration is allowed", method: "oidc", wantStatus: http.StatusOK},
		{name: "pending account email is forbidden", method: "code", pendingEmail: "victim@example.test", wantStatus: http.StatusConflict, wantReason: "registration_pending_email_forbidden"},
		{name: "passkey registration is denied", method: "passkey", wantStatus: http.StatusConflict, wantReason: "registration_method_denied"},
		{name: "unknown registration method fails closed", method: "unsupported", wantStatus: http.StatusForbidden, wantReason: "registration_method_unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := json.Marshal(CredentialRegistrationRequest{
				IdentityID:   hookIdentityID,
				Email:        "member@example.test",
				Method:       tt.method,
				PendingEmail: tt.pendingEmail,
				FlowID:       "registration-flow-1",
				FlowType:     "browser",
			})
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, "/hooks/reject-credential-registration", bytes.NewReader(payload))
			response := httptest.NewRecorder()

			handler := &HooksHandler{
				registrationHooks: authentication.NewRegistrationHookPolicy(
					hookRegistrationReuseHoldChecker{check: func(context.Context, string) (bool, error) {
						return false, nil
					}},
				),
			}
			handler.RejectCredentialRegistration(response, request)

			require.Equal(t, tt.wantStatus, response.Code, response.Body.String())
			if tt.wantReason != "" {
				require.Contains(t, response.Body.String(), tt.wantReason)
			}
		})
	}
}

func TestRejectCredentialRegistrationBlocksHeldEmailForCodeAndOIDCWithoutLifecycleDisclosure(t *testing.T) {
	var responses []*httptest.ResponseRecorder
	for _, method := range []string{"code", "oidc"} {
		payload, err := json.Marshal(CredentialRegistrationRequest{
			IdentityID: hookIdentityID,
			Email:      " Former@Example.COM ",
			Method:     method,
			FlowID:     "registration-flow-1",
			FlowType:   "browser",
		})
		require.NoError(t, err)
		request := httptest.NewRequest(http.MethodPost, "/hooks/reject-credential-registration", bytes.NewReader(payload))
		response := httptest.NewRecorder()
		handler := &HooksHandler{
			registrationHooks: authentication.NewRegistrationHookPolicy(
				hookRegistrationReuseHoldChecker{check: func(_ context.Context, email string) (bool, error) {
					require.Equal(t, " Former@Example.COM ", email)
					return true, nil
				}},
			),
		}

		handler.RejectCredentialRegistration(response, request)

		require.Equal(t, http.StatusConflict, response.Code)
		require.Contains(t, response.Body.String(), "registration_unavailable")
		require.NotContains(t, strings.ToLower(response.Body.String()), "deleted")
		require.NotContains(t, strings.ToLower(response.Body.String()), "reuse")
		responses = append(responses, response)
	}
	require.JSONEq(t, responses[0].Body.String(), responses[1].Body.String())
}

func TestRejectCredentialRegistrationFailsClosedWhenReuseHoldCannotBeChecked(t *testing.T) {
	payload, err := json.Marshal(CredentialRegistrationRequest{
		IdentityID: hookIdentityID,
		Email:      "member@example.test",
		Method:     "oidc",
		FlowID:     "registration-flow-1",
		FlowType:   "browser",
	})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/hooks/reject-credential-registration", bytes.NewReader(payload))
	response := httptest.NewRecorder()
	handler := &HooksHandler{
		registrationHooks: authentication.NewRegistrationHookPolicy(
			hookRegistrationReuseHoldChecker{check: func(context.Context, string) (bool, error) {
				return false, errors.New("database unavailable")
			}},
		),
	}

	handler.RejectCredentialRegistration(response, request)

	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.Contains(t, response.Body.String(), "registration_unavailable")
}

type hookRegistrationPolicy struct{ err error }

type hookRegistrationReuseHoldChecker struct {
	check func(context.Context, string) (bool, error)
}

type recordingLoginHookLifecycle struct {
	result authentication.LoginHookResult
	err    error
	inputs []authentication.LoginHookInput
}

func (checker hookRegistrationReuseHoldChecker) RegistrationEmailReuseBlocked(
	ctx context.Context,
	email string,
) (bool, error) {
	return checker.check(ctx, email)
}

func (lifecycle *recordingLoginHookLifecycle) Process(
	_ context.Context,
	input authentication.LoginHookInput,
) (authentication.LoginHookResult, error) {
	lifecycle.inputs = append(lifecycle.inputs, input)
	return lifecycle.result, lifecycle.err
}

func (policy *hookRegistrationPolicy) Validate(
	context.Context,
	authentication.RegistrationHookInput,
) error {
	return policy.err
}
