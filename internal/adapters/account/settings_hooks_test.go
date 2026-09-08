package accountadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/stretchr/testify/require"
)

const (
	hookIdentityID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	hookMemberID   = "bbbbbbbb-bbbb-4bbb-9bbb-bbbbbbbbbbbb"
)

func TestAfterSettingsMapsDurableTransitionConflicts(t *testing.T) {
	tests := []struct {
		name       string
		stageErr   error
		wantReason string
	}{
		{name: "address conflict", stageErr: account.ErrAccountEmailChangeConflict, wantReason: "email_change_conflict"},
		{name: "verified transition in flight", stageErr: account.ErrAccountEmailChangeInFlight, wantReason: "email_change_reconciliation_in_progress"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kratos := &hookIdentityManager{identity: &auth.Identity{
				ID: hookIdentityID, Traits: structured.Fields{"email": "old@example.test"},
			}}
			emails := &hookAccountEmailLifecycle{stageErr: tt.stageErr}
			handler := &SettingsHooksHandler{accountSettings: account.NewAccountSettingsHookService(kratos, emails)}

			response := performAfterSettings(handler, AfterSettingsRequest{
				FlowID:       "settings-flow-1",
				IdentityID:   hookIdentityID,
				Email:        "old@example.test",
				PendingEmail: "new@example.test",
			})

			require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), tt.wantReason)
			require.Len(t, emails.stageCalls, 1)
		})
	}
}

func TestAfterSettingsRejectsDirectCanonicalEmailMutation(t *testing.T) {
	kratos := &hookIdentityManager{identity: &auth.Identity{
		ID: hookIdentityID, Traits: structured.Fields{"email": "committed@example.test"},
	}}
	handler := &SettingsHooksHandler{accountSettings: account.NewAccountSettingsHookService(kratos, &hookAccountEmailLifecycle{})}

	response := performAfterSettings(handler, AfterSettingsRequest{
		FlowID:     "settings-flow-1",
		IdentityID: hookIdentityID,
		Email:      "candidate@example.test",
	})

	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "canonical_email_change_forbidden")
	require.Equal(t, "committed@example.test", kratos.identity.CurrentEmail())
}

func TestAfterSettingsStagesEmailWithoutCanonicalMutation(t *testing.T) {
	kratos := &hookIdentityManager{identity: &auth.Identity{
		ID: hookIdentityID, Traits: structured.Fields{"email": "old@example.test"},
	}}
	emails := &hookAccountEmailLifecycle{}
	handler := &SettingsHooksHandler{accountSettings: account.NewAccountSettingsHookService(kratos, emails)}

	response := performAfterSettings(handler, AfterSettingsRequest{
		FlowID:       "settings-flow-1",
		IdentityID:   hookIdentityID,
		Email:        "old@example.test",
		PendingEmail: "new@example.test",
	})

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Empty(t, kratos.updatedAccountEmails)
	require.Equal(t, []hookAccountEmailStageCall{{
		FlowID:           "settings-flow-1",
		IdentityID:       hookIdentityID,
		CurrentEmail:     "old@example.test",
		CandidatePending: "new@example.test",
	}}, emails.stageCalls)
}

func TestAfterVerificationDelegatesDurablyStagedTransition(t *testing.T) {
	emails := &hookAccountEmailLifecycle{}
	handler := &SettingsHooksHandler{accountSettings: account.NewAccountSettingsHookService(&hookIdentityManager{}, emails)}

	response := performAfterVerification(handler, AfterVerificationRequest{
		FlowID:       "verification-flow-1",
		IdentityID:   hookIdentityID,
		Email:        "old@example.test",
		PendingEmail: "new@example.test",
	})

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, []hookAccountEmailVerifyCall{{
		FlowID:       "verification-flow-1",
		IdentityID:   hookIdentityID,
		OldEmail:     "old@example.test",
		PendingEmail: "new@example.test",
	}}, emails.verifyCalls)
}

func TestAfterVerificationKeepsProofWhenNotificationPublishNeedsRetry(t *testing.T) {
	emails := &hookAccountEmailLifecycle{verifyErr: account.ErrAccountEmailChangeNotificationPublish}
	handler := &SettingsHooksHandler{accountSettings: account.NewAccountSettingsHookService(&hookIdentityManager{}, emails)}

	response := performAfterVerification(handler, AfterVerificationRequest{
		FlowID:       "verification-flow-1",
		IdentityID:   hookIdentityID,
		Email:        "old@example.test",
		PendingEmail: "new@example.test",
	})

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Len(t, emails.verifyCalls, 1)
}

// Keep the durable error mapping covered after replacing the old trait/profile
// hook tests.
func TestAfterVerificationMapsTransitionFailure(t *testing.T) {
	emails := &hookAccountEmailLifecycle{verifyErr: account.ErrAccountEmailChangeConflict}
	handler := &SettingsHooksHandler{accountSettings: account.NewAccountSettingsHookService(&hookIdentityManager{}, emails)}

	response := performAfterVerification(handler, AfterVerificationRequest{
		FlowID:       "verification-flow-1",
		IdentityID:   hookIdentityID,
		Email:        "old@example.test",
		PendingEmail: "new@example.test",
	})

	require.Equal(t, http.StatusConflict, response.Code)
	require.Contains(t, response.Body.String(), "email_change_conflict")
}

func TestAfterVerificationWithoutPendingEmailIsNoop(t *testing.T) {
	emails := &hookAccountEmailLifecycle{}
	handler := &SettingsHooksHandler{accountSettings: account.NewAccountSettingsHookService(&hookIdentityManager{}, emails)}

	response := performAfterVerification(handler, AfterVerificationRequest{
		FlowID:     "verification-flow-1",
		IdentityID: hookIdentityID,
		Email:      "old@example.test",
	})

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Empty(t, emails.verifyCalls)
}

func TestCredentialSettingsHooksFailClosedAtTheirOwnBoundary(t *testing.T) {
	t.Run("pre policy rejection", func(t *testing.T) {
		lifecycle := &recordingCredentialHookLifecycle{validateErr: account.ErrAccountCredentialUnrecoverable}
		handler := &SettingsHooksHandler{credentialHooks: lifecycle}
		response := performCredentialSettingsHook(handler.ValidateCredentials(account.AccountCredentialPasskey), "/hooks/pre-settings-passkey", CredentialSettingsHookRequest{
			IdentityID: hookIdentityID,
			FlowID:     "settings-flow-1",
			Credentials: map[string]auth.Credential{
				"passkey": {Type: "passkey", Identifiers: []string{"passkey-1"}},
			},
		}, "")
		require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "recoverable_auth_method")
	})

	t.Run("post completion retry", func(t *testing.T) {
		lifecycle := &recordingCredentialHookLifecycle{completeErr: errors.New("database unavailable")}
		handler := &SettingsHooksHandler{credentialHooks: lifecycle}
		response := performCredentialSettingsHook(handler.CompleteCredentials(account.AccountCredentialOIDC), "/hooks/post-settings-oidc", CredentialSettingsHookRequest{
			IdentityID:          hookIdentityID,
			FlowID:              "settings-flow-1",
			Credentials:         map[string]auth.Credential{},
			PreviousCredentials: map[string]auth.Credential{},
		}, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
		require.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "auth_method_completion_failed")
	})
}

func TestCredentialSettingsHooksKeepPreAndPostResponsibilitiesSeparate(t *testing.T) {
	credentials := map[string]auth.Credential{
		"code": {Type: "code", Identifiers: []string{"member@example.test"}},
	}
	previous := map[string]auth.Credential{
		"code": {Type: "code", Identifiers: []string{"member@example.test"}},
	}
	lifecycle := &recordingCredentialHookLifecycle{}
	handler := &SettingsHooksHandler{credentialHooks: lifecycle}
	payload := CredentialSettingsHookRequest{
		IdentityID:          hookIdentityID,
		FlowID:              "settings-flow-1",
		Credentials:         credentials,
		PreviousCredentials: previous,
	}

	pre := performCredentialSettingsHook(handler.ValidateCredentials(account.AccountCredentialOIDC), "/hooks/pre-settings-oidc", payload, "")
	require.Equal(t, http.StatusOK, pre.Code, pre.Body.String())
	require.Len(t, lifecycle.validated, 1)
	require.Empty(t, lifecycle.completed)
	require.Equal(t, account.AccountCredentialOIDC, lifecycle.validated[0].Kind)

	post := performCredentialSettingsHook(handler.CompleteCredentials(account.AccountCredentialPasskey), "/hooks/post-settings-passkey", payload, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	require.Equal(t, http.StatusOK, post.Code, post.Body.String())
	require.Len(t, lifecycle.validated, 1)
	require.Len(t, lifecycle.completed, 1)
	require.Equal(t, account.AccountCredentialPasskey, lifecycle.completed[0].Kind)
	require.Equal(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", lifecycle.completed[0].AuditID)
}

func TestNewSettingsHooksHandlerRequiresAccountPorts(t *testing.T) {
	require.Panics(t, func() { NewSettingsHooksHandler(nil, &recordingCredentialHookLifecycle{}) })
	require.Panics(t, func() { NewSettingsHooksHandler(&recordingAccountSettingsHookLifecycle{}, nil) })
}

type hookAccountEmailLifecycle struct {
	stageErr    error
	verifyErr   error
	stageCalls  []hookAccountEmailStageCall
	verifyCalls []hookAccountEmailVerifyCall
}

type hookAccountEmailStageCall struct {
	FlowID              string
	IdentityID          string
	CurrentEmail        string
	CurrentPendingEmail string
	CandidatePending    string
	PendingVerified     bool
}

type hookAccountEmailVerifyCall struct {
	FlowID       string
	IdentityID   string
	OldEmail     string
	PendingEmail string
}

type hookIdentityManager struct {
	identity             *auth.Identity
	err                  error
	updatedTraits        []structured.Fields
	updatedMetadata      []structured.Fields
	updatedAccountEmails []string
	deletedSessions      []string
}

type recordingAccountSettingsHookLifecycle struct {
	stageErr  error
	verifyErr error
}

type recordingCredentialHookLifecycle struct {
	validateErr error
	completeErr error
	validated   []account.AccountCredentialHookInput
	completed   []account.AccountCredentialHookInput
}

func (*hookIdentityManager) DeleteIdentity(context.Context, string) error { return nil }

func (*hookIdentityManager) GetIdentityEmail(context.Context, string) (string, error) {
	return "", nil
}

func (*hookIdentityManager) ListIdentities(context.Context, int, int) ([]*auth.Identity, int64, error) {
	return nil, 0, nil
}

func (*hookIdentityManager) SetIdentityState(context.Context, string, string) error {
	return nil
}

func (*hookIdentityManager) UpdateIdentityExternalID(context.Context, string, string) error {
	return nil
}

func (*hookIdentityManager) UpdateIdentityVerifiableAddresses(context.Context, string, []auth.VerifiableAddress) error {
	return nil
}

func (l *hookAccountEmailLifecycle) StageOrCancel(
	_ context.Context,
	flowID, identityID, currentEmail, currentPendingEmail, candidatePending string,
	pendingVerified bool,
) error {
	l.stageCalls = append(l.stageCalls, hookAccountEmailStageCall{
		FlowID:              flowID,
		IdentityID:          identityID,
		CurrentEmail:        currentEmail,
		CurrentPendingEmail: currentPendingEmail,
		CandidatePending:    candidatePending,
		PendingVerified:     pendingVerified,
	})
	return l.stageErr
}

func (l *hookAccountEmailLifecycle) VerifyAndReconcile(
	_ context.Context,
	flowID, identityID, oldEmail, pendingEmail string,
) error {
	l.verifyCalls = append(l.verifyCalls, hookAccountEmailVerifyCall{
		FlowID: flowID, IdentityID: identityID, OldEmail: oldEmail, PendingEmail: pendingEmail,
	})
	return l.verifyErr
}

func (lifecycle *recordingAccountSettingsHookLifecycle) Stage(
	context.Context,
	account.AccountSettingsHookInput,
) error {
	return lifecycle.stageErr
}

func (lifecycle *recordingAccountSettingsHookLifecycle) Verify(
	context.Context,
	account.AccountSettingsHookInput,
) error {
	return lifecycle.verifyErr
}

func (lifecycle *recordingCredentialHookLifecycle) Complete(
	_ context.Context,
	input account.AccountCredentialHookInput,
) error {
	lifecycle.completed = append(lifecycle.completed, input)
	return lifecycle.completeErr
}

func (lifecycle *recordingCredentialHookLifecycle) Validate(
	_ context.Context,
	input account.AccountCredentialHookInput,
) error {
	lifecycle.validated = append(lifecycle.validated, input)
	return lifecycle.validateErr
}

func (m *hookIdentityManager) DeleteIdentitySessions(_ context.Context, identityID string) error {
	m.deletedSessions = append(m.deletedSessions, identityID)
	return nil
}

func (m *hookIdentityManager) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return m.identity, m.err
}

func (m *hookIdentityManager) GetIdentityWithIncludeCredential(context.Context, string, string) (*auth.Identity, error) {
	return m.identity, m.err
}

func (m *hookIdentityManager) UpdateIdentityAccountEmailState(
	_ context.Context,
	_ string,
	currentEmail *string,
	_ structured.Fields,
	_ []auth.VerifiableAddress,
) error {
	if currentEmail == nil {
		return errors.New("current email is required")
	}
	m.updatedAccountEmails = append(m.updatedAccountEmails, *currentEmail)
	return nil
}

func (m *hookIdentityManager) UpdateIdentityMetadataAdmin(_ context.Context, _ string, metadata structured.Fields) error {
	m.updatedMetadata = append(m.updatedMetadata, metadata)
	return nil
}

func (m *hookIdentityManager) UpdateIdentityTraits(_ context.Context, _ string, traits structured.Fields) error {
	m.updatedTraits = append(m.updatedTraits, traits)
	return nil
}

var _ account.AccountEmailChangeHookLifecycle = (*hookAccountEmailLifecycle)(nil)

func performAfterSettings(handler *SettingsHooksHandler, payload AfterSettingsRequest) *httptest.ResponseRecorder {
	body, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/hooks/after-settings", bytes.NewReader(body))
	response := httptest.NewRecorder()
	handler.AfterSettings(response, request)
	return response
}

func performAfterVerification(handler *SettingsHooksHandler, payload AfterVerificationRequest) *httptest.ResponseRecorder {
	body, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/hooks/after-verification", bytes.NewReader(body))
	request.Header.Set("Ory-Webhook-Trigger-ID", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	response := httptest.NewRecorder()
	handler.AfterVerification(response, request)
	return response
}

func performCredentialSettingsHook(
	hook http.HandlerFunc,
	path string,
	payload CredentialSettingsHookRequest,
	triggerID string,
) *httptest.ResponseRecorder {
	body, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	if triggerID != "" {
		request.Header.Set("Ory-Webhook-Trigger-ID", triggerID)
	}
	response := httptest.NewRecorder()
	hook(response, request)
	return response
}
