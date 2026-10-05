package accountadapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/adapters/kratoswebhook"
	"github.com/echovisionlab/geul-api/internal/auth"
)

type AfterSettingsRequest struct {
	FlowID       string `json:"flow_id"`
	IdentityID   string `json:"identity_id"`
	Email        string `json:"email"`
	PendingEmail string `json:"pending_email"`
}

type AfterVerificationRequest struct {
	FlowID       string `json:"flow_id"`
	IdentityID   string `json:"identity_id"`
	Email        string `json:"email"`
	PendingEmail string `json:"pending_email"`
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

// SettingsHooksHandler translates Kratos settings transport into Account operations.
// Policy, auditing, and diagnostic classification belong to Account.
type SettingsHooksHandler struct {
	accountSettings accountSettingsHookLifecycle
	credentialHooks accountCredentialHookLifecycle
}

type accountCredentialHookLifecycle interface {
	Validate(context.Context, account.AccountCredentialHookInput) error
	Complete(context.Context, account.AccountCredentialHookInput) error
}

type accountSettingsHookLifecycle interface {
	Stage(context.Context, account.AccountSettingsHookInput) error
	Verify(context.Context, account.AccountSettingsHookInput) error
}

func NewSettingsHooksHandler(settings accountSettingsHookLifecycle, credentials accountCredentialHookLifecycle) *SettingsHooksHandler {
	if settings == nil || credentials == nil {
		panic("account settings hook application ports are required")
	}
	return &SettingsHooksHandler{accountSettings: settings, credentialHooks: credentials}
}

func (h *SettingsHooksHandler) RegisterRoutes(mux *http.ServeMux, protect func(http.HandlerFunc) http.Handler) {
	mux.Handle("/hooks/after-settings", protect(h.AfterSettings))
	mux.Handle("/hooks/after-verification", protect(h.AfterVerification))
	mux.Handle("/hooks/pre-settings-oidc", protect(h.ValidateCredentials(account.AccountCredentialOIDC)))
	mux.Handle("/hooks/post-settings-oidc", protect(h.CompleteCredentials(account.AccountCredentialOIDC)))
	mux.Handle("/hooks/pre-settings-passkey", protect(h.ValidateCredentials(account.AccountCredentialPasskey)))
	mux.Handle("/hooks/post-settings-passkey", protect(h.CompleteCredentials(account.AccountCredentialPasskey)))
}

// AfterSettings completes profile settings without mutating canonical account
// email. A changed pending_email remains verification-only until the
// after-verification lifecycle applies it.
func (h *SettingsHooksHandler) AfterSettings(w http.ResponseWriter, r *http.Request) {
	if !kratoswebhook.RequirePost(w, r) {
		return
	}

	var req AfterSettingsRequest
	if !kratoswebhook.Decode(w, r, &req, "Failed to decode after-settings request") {
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
			kratoswebhook.WriteError(w, http.StatusInternalServerError, "Could not validate account settings.", "canonical_email_guard_failed")
			return
		}
		if errors.Is(err, account.ErrCanonicalEmailChangeForbidden) {
			kratoswebhook.WriteError(w, http.StatusConflict, "Change email through the verification flow.", "canonical_email_change_forbidden")
			return
		}
		if errors.Is(err, account.ErrAccountEmailChangeConflict) {
			kratoswebhook.WriteError(
				w,
				http.StatusConflict,
				"That email address is already linked to another account.",
				"email_change_conflict",
			)
			return
		}
		if errors.Is(err, account.ErrAccountEmailChangeInFlight) {
			kratoswebhook.WriteError(
				w,
				http.StatusConflict,
				"A verified email change is still being applied.",
				"email_change_reconciliation_in_progress",
			)
			return
		}
		slog.ErrorContext(r.Context(), "Failed to stage account email change", "error", err, "identity_id", req.IdentityID, "flow_id", req.FlowID)
		kratoswebhook.WriteError(w, http.StatusInternalServerError, "Could not stage the account email change.", "email_change_stage_failed")
		return
	}

	kratoswebhook.WriteEmpty(w)
}

// AfterVerification completes a staged canonical-email change only after
// Kratos has persisted verification of that exact pending address.
func (h *SettingsHooksHandler) AfterVerification(w http.ResponseWriter, r *http.Request) {
	if !kratoswebhook.RequirePost(w, r) {
		return
	}

	var req AfterVerificationRequest
	if !kratoswebhook.Decode(w, r, &req, "Failed to decode after-verification request") {
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
		slog.ErrorContext(r.Context(), "Failed to reconcile verified pending email", "error", err, "identity_id", req.IdentityID, "flow_id", req.FlowID)
		if errors.Is(err, account.ErrAccountEmailChangeConflict) {
			kratoswebhook.WriteError(w, http.StatusConflict, "That email address is already linked to another account.", "email_change_conflict")
			return
		}
		if errors.Is(err, account.ErrAccountEmailChangeNotificationPublish) {
			// Kratos verification and the canonical account projection already
			// succeeded. Keep the active request for the bounded reconciler/manual
			// replay path without turning the user's successful proof into a 400.
			kratoswebhook.WriteEmpty(w)
			return
		}
		kratoswebhook.WriteError(w, http.StatusInternalServerError, "Could not apply the verified email address.", "email_change_apply_failed")
		return
	}

	kratoswebhook.WriteEmpty(w)
}

func (h *SettingsHooksHandler) CompleteCredentials(kind account.AccountCredentialKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { h.completeSettingsCredentialMutation(w, r, kind) }
}

// ValidateCredentials binds a credential kind at route registration, not from user input.
func (h *SettingsHooksHandler) ValidateCredentials(kind account.AccountCredentialKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { h.validateSettingsCredentialMutation(w, r, kind) }
}

func (h *SettingsHooksHandler) completeSettingsCredentialMutation(
	w http.ResponseWriter,
	r *http.Request,
	kind account.AccountCredentialKind,
) {
	if !kratoswebhook.RequirePost(w, r) {
		return
	}
	var req CredentialSettingsHookRequest
	if !kratoswebhook.Decode(w, r, &req, "Failed to decode post-settings credential request") {
		return
	}
	auditID := strings.TrimSpace(r.Header.Get("Ory-Webhook-Trigger-ID"))
	input := credentialHookInput(req, kind, auditID)
	if err := h.credentialHooks.Complete(r.Context(), input); err != nil {
		kratoswebhook.WriteError(w, http.StatusInternalServerError, "Could not complete the sign-in method change.", "auth_method_completion_failed")
		return
	}
	kratoswebhook.WriteEmpty(w)
}

func (h *SettingsHooksHandler) validateSettingsCredentialMutation(
	w http.ResponseWriter,
	r *http.Request,
	kind account.AccountCredentialKind,
) {
	if !kratoswebhook.RequirePost(w, r) {
		return
	}
	var req CredentialSettingsHookRequest
	if !kratoswebhook.Decode(w, r, &req, "Failed to decode pre-settings credential request") {
		return
	}
	input := credentialHookInput(req, kind, "")
	if err := h.credentialHooks.Validate(r.Context(), input); err != nil {
		switch {
		case errors.Is(err, account.ErrAccountCredentialUnrecoverable):
			kratoswebhook.WriteError(w, http.StatusForbidden, "Keep email sign-in or another social sign-in method connected.", "recoverable_auth_method")
		case errors.Is(err, account.ErrMemberPrimaryEmailUnavailable):
			kratoswebhook.WriteError(w, http.StatusConflict, "Choose another account email before disconnecting the provider that proves it.", "canonical_email_provider_required")
		default:
			kratoswebhook.WriteError(w, http.StatusInternalServerError, "Could not validate the sign-in method change.", "auth_method_validation_failed")
		}
		return
	}
	kratoswebhook.WriteEmpty(w)
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
