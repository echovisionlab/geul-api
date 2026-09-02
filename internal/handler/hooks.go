package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// HooksHandler handles webhook endpoints
type HooksHandler struct {
	loginHooks        loginHookLifecycle
	registrationHooks registrationHookPolicy
	accountSettings   accountSettingsHookLifecycle
	credentialHooks   accountCredentialHookLifecycle
}

type accountCredentialHookLifecycle interface {
	Validate(context.Context, account.AccountCredentialHookInput) error
	Complete(context.Context, account.AccountCredentialHookInput) error
}

type loginHookLifecycle interface {
	Process(context.Context, authentication.LoginHookInput) (authentication.LoginHookResult, error)
}

type registrationHookPolicy interface {
	Validate(context.Context, authentication.RegistrationHookInput) error
}

type accountSettingsHookLifecycle interface {
	Stage(context.Context, account.AccountSettingsHookInput) error
	Verify(context.Context, account.AccountSettingsHookInput) error
}

// NewHooksHandler creates a new HooksHandler
func NewHooksHandler(
	loginHooks loginHookLifecycle,
	registrationHooks registrationHookPolicy,
	accountSettings accountSettingsHookLifecycle,
	credentialHooks accountCredentialHookLifecycle,
) *HooksHandler {
	if loginHooks == nil || registrationHooks == nil || accountSettings == nil || credentialHooks == nil {
		panic("login, registration, account settings, and credential hook application ports are required")
	}
	return &HooksHandler{
		loginHooks: loginHooks, registrationHooks: registrationHooks,
		accountSettings: accountSettings, credentialHooks: credentialHooks,
	}
}

// AfterLoginRequest is the request body for the after-login webhook
type AfterLoginRequest struct {
	IdentityID      string `json:"identity_id"`
	Email           string `json:"email"`
	PreferredLocale string `json:"preferred_locale,omitempty"`
	Trigger         string `json:"trigger,omitempty"`
}

// AfterLoginErrorResponse is returned when login should be rejected
type AfterLoginErrorResponse struct {
	Error     string  `json:"error"`
	ErrorCode string  `json:"error_code"`
	Banned    bool    `json:"banned,omitempty"`
	BanReason *string `json:"ban_reason,omitempty"`
}

type CredentialRegistrationRequest struct {
	IdentityID   string `json:"identity_id"`
	Email        string `json:"email"`
	PendingEmail string `json:"pending_email"`
	Method       string `json:"method"`
	FlowID       string `json:"flow_id"`
	FlowType     string `json:"flow_type"`
}

func requirePostHook(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodPost {
		return true
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	return false
}

func decodeHookRequest(
	w http.ResponseWriter,
	r *http.Request,
	destination structured.Value, logMessage string,
) bool {
	if err := json.NewDecoder(r.Body).Decode(destination); err != nil {
		slog.Error(logMessage, "error", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

func writeBannedAfterLoginResponse(
	w http.ResponseWriter,
	banReason *string,
) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	if err := json.NewEncoder(w).Encode(AfterLoginErrorResponse{
		Error:     "Your account has been suspended.",
		ErrorCode: "account_banned",
		Banned:    true,
		BanReason: banReason,
	}); err != nil {
		slog.Error("Failed to encode banned response", "error", err)
	}
}

// AfterLogin handles the after-login webhook from Kratos.
// It validates the linked account identity and checks ban status.
// Subscriber opt-in state is intentionally not changed by login.
// Returns 403 if user is banned (causes Kratos to reject the session)
func (h *HooksHandler) AfterLogin(w http.ResponseWriter, r *http.Request) {
	if !requirePostHook(w, r) {
		return
	}

	var req AfterLoginRequest
	if !decodeHookRequest(w, r, &req, "Failed to decode after-login request") {
		return
	}

	result, err := h.loginHooks.Process(r.Context(), authentication.LoginHookInput{
		IdentityID: req.IdentityID, Email: req.Email,
		PreferredLocale: req.PreferredLocale, Trigger: req.Trigger,
	})
	if err != nil {
		if errors.Is(err, authentication.ErrLoginHookInput) {
			http.Error(w, "Missing identity_id", http.StatusBadRequest)
			return
		}
		operation, responseMessage := loginHookFailureResponse(err)
		slog.Error("After-login lifecycle failed", "operation", operation, "error", err, "identity_id", req.IdentityID)
		http.Error(w, responseMessage, http.StatusInternalServerError)
		return
	}
	if result.Banned {
		writeBannedAfterLoginResponse(w, result.BanReason)
		return
	}

	writeEmptyHookResponse(w)
}

func loginHookFailureResponse(err error) (string, string) {
	switch {
	case errors.Is(err, authentication.ErrLoginIdentityLoad):
		return "load identity from Kratos", "Failed to get user identity"
	case errors.Is(err, authentication.ErrLoginMemberProvision):
		return "provision registration member", "Failed to create member account"
	case errors.Is(err, authentication.ErrLoginMemberValidation):
		return "validate identity member link", "Failed to validate member account"
	case errors.Is(err, authentication.ErrLoginRoleSynchronization):
		return "sync login role", "Failed to sync user role"
	default:
		return "process after-login lifecycle", "Failed to validate member account"
	}
}

// RejectCredentialRegistration is the fail-closed policy boundary for public
// Kratos registration methods. Email-code and OIDC registration are allowed;
// passkeys may only be added after an account exists through authenticated
// account settings.
func (h *HooksHandler) RejectCredentialRegistration(w http.ResponseWriter, r *http.Request) {
	if !requirePostHook(w, r) {
		return
	}

	var req CredentialRegistrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("Failed to decode credential-registration policy request", "error", err)
		writeKratosHookError(w, http.StatusBadRequest, "Could not validate the registration method.", "registration_method_invalid")
		return
	}
	err := h.registrationHooks.Validate(r.Context(), authentication.RegistrationHookInput{
		Email: req.Email, PendingEmail: req.PendingEmail, Method: req.Method,
	})
	switch {
	case err == nil:
		writeEmptyHookResponse(w)
	case errors.Is(err, authentication.ErrRegistrationPendingEmail):
		writeKratosHookError(
			w,
			http.StatusConflict,
			"An account email can only be changed from account settings.",
			"registration_pending_email_forbidden",
		)
	case errors.Is(err, authentication.ErrRegistrationMethodDenied):
		writeKratosHookError(w, http.StatusConflict, "Use email verification or social sign-in to create an account.", "registration_method_denied")
	case errors.Is(err, authentication.ErrRegistrationMethodUnknown):
		writeKratosHookError(w, http.StatusForbidden, "This registration method is not available.", "registration_method_unknown")
	case errors.Is(err, authentication.ErrRegistrationReuseHeld):
		writeKratosHookError(w, http.StatusConflict, "Registration could not be completed.", "registration_unavailable")
	case errors.Is(err, authentication.ErrRegistrationUnavailable):
		slog.Error("Registration email reuse policy unavailable", "error", err, "method", req.Method, "flow_id", req.FlowID)
		writeKratosHookError(w, http.StatusInternalServerError, "Registration could not be completed.", "registration_unavailable")
	default:
		slog.Error("Registration policy failed", "error", err, "method", req.Method, "flow_id", req.FlowID)
		writeKratosHookError(w, http.StatusInternalServerError, "Registration could not be completed.", "registration_unavailable")
	}
}

type CredentialSettingsHookRequest struct {
	IdentityID                string                     `json:"identity_id"`
	FlowID                    string                     `json:"flow_id,omitempty"`
	FlowType                  string                     `json:"flow_type"`
	Credentials               map[string]auth.Credential `json:"credentials"`
	CredentialSnapshotPresent *bool                      `json:"credentials_present,omitempty"`
	PreviousCredentials       map[string]auth.Credential `json:"previous_credentials"`
	PreviousSnapshotPresent   *bool                      `json:"previous_credentials_present,omitempty"`
}

type AfterSettingsRequest struct {
	FlowID       string `json:"flow_id"`
	IdentityID   string `json:"identity_id"`
	Email        string `json:"email"`
	PendingEmail string `json:"pending_email"`
}

type kratosHookMessage struct {
	ID      int               `json:"id"`
	Text    string            `json:"text"`
	Type    string            `json:"type"`
	Context map[string]string `json:"context,omitempty"`
}

type kratosHookMessageGroup struct {
	InstancePtr string              `json:"instance_ptr"`
	Messages    []kratosHookMessage `json:"messages"`
}

type kratosHookErrorResponse struct {
	Messages []kratosHookMessageGroup `json:"messages"`
}

type AfterVerificationRequest struct {
	FlowID       string `json:"flow_id"`
	IdentityID   string `json:"identity_id"`
	Email        string `json:"email"`
	PendingEmail string `json:"pending_email"`
}

// AfterSettings completes profile settings without mutating canonical account
// email. A changed pending_email remains verification-only until the
// after-verification lifecycle applies it.
func (h *HooksHandler) AfterSettings(w http.ResponseWriter, r *http.Request) {
	if !requirePostHook(w, r) {
		return
	}

	var req AfterSettingsRequest
	if !decodeHookRequest(w, r, &req, "Failed to decode after-settings request") {
		return
	}
	err := h.accountSettings.Stage(r.Context(), account.AccountSettingsHookInput{
		FlowID: req.FlowID, IdentityID: req.IdentityID, Email: req.Email, PendingEmail: req.PendingEmail,
	})
	if err != nil {
		if errors.Is(err, account.ErrAccountSettingsHookInput) {
			http.Error(w, "Missing identity_id or flow_id", http.StatusBadRequest)
			return
		}
		if errors.Is(err, account.ErrCanonicalEmailGuardFailed) {
			writeKratosHookError(w, http.StatusInternalServerError, "Could not validate account settings.", "canonical_email_guard_failed")
			return
		}
		if errors.Is(err, account.ErrCanonicalEmailChangeForbidden) {
			writeKratosHookError(w, http.StatusConflict, "Change email through the verification flow.", "canonical_email_change_forbidden")
			return
		}
		if errors.Is(err, account.ErrAccountEmailChangeConflict) {
			writeKratosHookError(
				w,
				http.StatusConflict,
				"That email address is already linked to another account.",
				"email_change_conflict",
			)
			return
		}
		if errors.Is(err, account.ErrAccountEmailChangeInFlight) {
			writeKratosHookError(
				w,
				http.StatusConflict,
				"A verified email change is still being applied.",
				"email_change_reconciliation_in_progress",
			)
			return
		}
		slog.Error("Failed to stage account email change", "error", err, "identity_id", req.IdentityID, "flow_id", req.FlowID)
		writeKratosHookError(w, http.StatusInternalServerError, "Could not stage the account email change.", "email_change_stage_failed")
		return
	}

	writeEmptyHookResponse(w)
}

// AfterVerification completes a staged canonical-email change only after
// Kratos has persisted verification of that exact pending address.
func (h *HooksHandler) AfterVerification(w http.ResponseWriter, r *http.Request) {
	if !requirePostHook(w, r) {
		return
	}

	var req AfterVerificationRequest
	if !decodeHookRequest(w, r, &req, "Failed to decode after-verification request") {
		return
	}

	err := h.accountSettings.Verify(r.Context(), account.AccountSettingsHookInput{
		FlowID: req.FlowID, IdentityID: req.IdentityID, Email: req.Email, PendingEmail: req.PendingEmail,
	})
	if err != nil {
		if errors.Is(err, account.ErrAccountSettingsHookInput) {
			http.Error(w, "Missing identity_id or flow_id", http.StatusBadRequest)
			return
		}
		slog.Error("Failed to reconcile verified pending email", "error", err, "identity_id", req.IdentityID, "flow_id", req.FlowID)
		if errors.Is(err, account.ErrAccountEmailChangeConflict) {
			writeKratosHookError(w, http.StatusConflict, "That email address is already linked to another account.", "email_change_conflict")
			return
		}
		if errors.Is(err, account.ErrAccountEmailChangeNotificationPublish) {
			// Kratos verification and the canonical account projection already
			// succeeded. Keep the active request for the bounded reconciler/manual
			// replay path without turning the user's successful proof into a 400.
			writeEmptyHookResponse(w)
			return
		}
		writeKratosHookError(w, http.StatusInternalServerError, "Could not apply the verified email address.", "email_change_apply_failed")
		return
	}

	writeEmptyHookResponse(w)
}

func writeEmptyHookResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(structured.Fields{}); err != nil {
		slog.Error("Failed to encode hook response", "error", err)
	}
}

// PreSettingsOIDC is the fail-closed policy hook. It validates the proposed
// full credential snapshot without applying product state or side effects.
func (h *HooksHandler) PreSettingsOIDC(w http.ResponseWriter, r *http.Request) {
	h.validateSettingsCredentialMutation(w, r, account.AccountCredentialOIDC)
}

// PreSettingsPasskey applies the same recoverability policy to a proposed
// passkey settings snapshot before Kratos persists it.
func (h *HooksHandler) PreSettingsPasskey(w http.ResponseWriter, r *http.Request) {
	h.validateSettingsCredentialMutation(w, r, account.AccountCredentialPasskey)
}

func (h *HooksHandler) validateSettingsCredentialMutation(
	w http.ResponseWriter,
	r *http.Request,
	kind account.AccountCredentialKind,
) {
	if !requirePostHook(w, r) {
		return
	}
	var req CredentialSettingsHookRequest
	if !decodeHookRequest(w, r, &req, "Failed to decode pre-settings credential request") {
		return
	}
	if err := h.credentialHooks.Validate(r.Context(), credentialHookInput(req, kind, "")); err != nil {
		switch {
		case errors.Is(err, account.ErrAccountCredentialUnrecoverable):
			writeKratosHookError(w, http.StatusForbidden, "Keep email sign-in or another social sign-in method connected.", "recoverable_auth_method")
		case errors.Is(err, account.ErrMemberPrimaryEmailUnavailable):
			writeKratosHookError(w, http.StatusConflict, "Choose another account email before disconnecting the provider that proves it.", "canonical_email_provider_required")
		default:
			slog.Error("Failed to validate settings credential mutation", "error", err, "identity_id", req.IdentityID, "credential_type", kind)
			writeKratosHookError(w, http.StatusInternalServerError, "Could not validate the sign-in method change.", "auth_method_validation_failed")
		}
		return
	}
	writeEmptyHookResponse(w)
}

// PostSettingsOIDC completes the exact committed OIDC transition.
func (h *HooksHandler) PostSettingsOIDC(w http.ResponseWriter, r *http.Request) {
	h.completeSettingsCredentialMutation(w, r, account.AccountCredentialOIDC)
}

// PostSettingsPasskey completes the exact committed passkey transition.
func (h *HooksHandler) PostSettingsPasskey(w http.ResponseWriter, r *http.Request) {
	h.completeSettingsCredentialMutation(w, r, account.AccountCredentialPasskey)
}

func (h *HooksHandler) completeSettingsCredentialMutation(
	w http.ResponseWriter,
	r *http.Request,
	kind account.AccountCredentialKind,
) {
	if !requirePostHook(w, r) {
		return
	}
	var req CredentialSettingsHookRequest
	if !decodeHookRequest(w, r, &req, "Failed to decode post-settings credential request") {
		return
	}
	auditID := strings.TrimSpace(r.Header.Get("Ory-Webhook-Trigger-ID"))
	if err := h.credentialHooks.Complete(r.Context(), credentialHookInput(req, kind, auditID)); err != nil {
		slog.Error("Failed to complete settings credential mutation", "error", err, "identity_id", req.IdentityID, "credential_type", kind, "audit_id", auditID)
		writeKratosHookError(w, http.StatusInternalServerError, "Could not complete the sign-in method change.", "auth_method_completion_failed")
		return
	}
	writeEmptyHookResponse(w)
}

func credentialHookInput(
	req CredentialSettingsHookRequest,
	kind account.AccountCredentialKind,
	auditID string,
) account.AccountCredentialHookInput {
	credentialSnapshotPresent := req.Credentials != nil
	if req.CredentialSnapshotPresent != nil {
		credentialSnapshotPresent = *req.CredentialSnapshotPresent
	}
	previousSnapshotPresent := req.PreviousCredentials != nil
	if req.PreviousSnapshotPresent != nil {
		previousSnapshotPresent = *req.PreviousSnapshotPresent
	}
	return account.AccountCredentialHookInput{
		AuditID:                   auditID,
		FlowID:                    req.FlowID,
		IdentityID:                req.IdentityID,
		Kind:                      kind,
		PreviousCredentials:       req.PreviousCredentials,
		Credentials:               req.Credentials,
		PreviousSnapshotPresent:   previousSnapshotPresent,
		CredentialSnapshotPresent: credentialSnapshotPresent,
	}
}

func writeKratosHookError(w http.ResponseWriter, status int, message string, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(kratosHookErrorResponse{
		Messages: []kratosHookMessageGroup{
			{
				InstancePtr: "#/method",
				Messages: []kratosHookMessage{
					{
						ID:   4900001,
						Text: message,
						Type: "error",
						Context: map[string]string{
							"reason": code,
						},
					},
				},
			},
		},
	}); err != nil {
		slog.Error("Failed to encode Kratos hook error response", "error", err)
	}
}
