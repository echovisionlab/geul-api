package authentication

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/echovisionlab/geul-api/internal/adapters/kratoswebhook"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// AfterLoginErrorResponse is returned when login should be rejected
type AfterLoginErrorResponse struct {
	Error     string  `json:"error"`
	ErrorCode string  `json:"error_code"`
	Banned    bool    `json:"banned,omitempty"`
	BanReason *string `json:"ban_reason,omitempty"`
}

// AfterLoginRequest is the request body for the after-login webhook
type AfterLoginRequest struct {
	IdentityID      string `json:"identity_id"`
	Email           string `json:"email"`
	PreferredLocale string `json:"preferred_locale,omitempty"`
	Trigger         string `json:"trigger,omitempty"`
}

type CredentialRegistrationRequest struct {
	IdentityID   string `json:"identity_id"`
	Email        string `json:"email"`
	PendingEmail string `json:"pending_email"`
	Method       string `json:"method"`
	FlowID       string `json:"flow_id"`
	FlowType     string `json:"flow_type"`
}

// HooksHandler adapts Kratos login and registration to Authentication's application ports.
type HooksHandler struct {
	loginHooks        loginHookLifecycle
	registrationHooks registrationHookPolicy
}

type loginHookLifecycle interface {
	Process(context.Context, authentication.LoginHookInput) (authentication.LoginHookResult, error)
}

type registrationHookPolicy interface {
	Validate(context.Context, authentication.RegistrationHookInput) error
}

func NewHooksHandler(login loginHookLifecycle, registration registrationHookPolicy) *HooksHandler {
	if login == nil || registration == nil {
		panic("authentication hook application ports are required")
	}
	return &HooksHandler{loginHooks: login, registrationHooks: registration}
}

func (h *HooksHandler) RegisterRoutes(mux *http.ServeMux, protect func(http.HandlerFunc) http.Handler) {
	mux.Handle("/hooks/after-login", protect(h.AfterLogin))
	mux.Handle("/hooks/reject-credential-registration", protect(h.RejectCredentialRegistration))
}

// AfterLogin handles the after-login webhook from Kratos.
// It validates the linked account identity and checks ban status.
// Subscriber opt-in state is intentionally not changed by login.
// Returns 403 if user is banned (causes Kratos to reject the session)
func (h *HooksHandler) AfterLogin(w http.ResponseWriter, r *http.Request) {
	if !kratoswebhook.RequirePost(w, r) {
		return
	}

	var req AfterLoginRequest
	if !kratoswebhook.Decode(w, r, &req) {
		return
	}

	result, err := h.loginHooks.Process(r.Context(), authentication.LoginHookInput{
		IdentityID: req.IdentityID, Email: req.Email,
		PreferredLocale: req.PreferredLocale, Trigger: req.Trigger,
	})
	if err != nil {
		if errors.Is(err, authentication.ErrLoginHookInput) {
			recordHookFailure(r, err, slog.String("identity_id", req.IdentityID))
			http.Error(w, "Missing identity_id", http.StatusBadRequest)
			return
		}
		recordHookFailure(r, err, slog.String("identity_id", req.IdentityID))
		http.Error(w, loginHookFailureResponse(err), http.StatusInternalServerError)
		return
	}
	if result.Banned {
		writeBannedAfterLoginResponse(w, r, result.BanReason)
		return
	}

	kratoswebhook.WriteEmpty(r.Context(), w)
}

// RejectCredentialRegistration is the fail-closed policy boundary for public
// Kratos registration methods. Email-code and OIDC registration are allowed;
// passkeys may only be added after an account exists through authenticated
// account settings.
func (h *HooksHandler) RejectCredentialRegistration(w http.ResponseWriter, r *http.Request) {
	if !kratoswebhook.RequirePost(w, r) {
		return
	}

	var req CredentialRegistrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		recordHookFailure(r, err)
		kratoswebhook.WriteError(r.Context(), w, http.StatusBadRequest, "Could not validate the registration method.", "registration_method_invalid")
		return
	}
	err := h.registrationHooks.Validate(r.Context(), authentication.RegistrationHookInput{
		Email: req.Email, PendingEmail: req.PendingEmail, Method: req.Method,
	})
	if err != nil {
		recordHookFailure(r, err, slog.String("method", req.Method), slog.String("flow_id", req.FlowID))
	}
	switch {
	case err == nil:
		kratoswebhook.WriteEmpty(r.Context(), w)
	case errors.Is(err, authentication.ErrRegistrationPendingEmail):
		kratoswebhook.WriteError(
			r.Context(),
			w,
			http.StatusConflict,
			"An account email can only be changed from account settings.",
			"registration_pending_email_forbidden",
		)
	case errors.Is(err, authentication.ErrRegistrationMethodDenied):
		kratoswebhook.WriteError(r.Context(), w, http.StatusConflict, "Use email verification or social sign-in to create an account.", "registration_method_denied")
	case errors.Is(err, authentication.ErrRegistrationMethodUnknown):
		kratoswebhook.WriteError(r.Context(), w, http.StatusForbidden, "This registration method is not available.", "registration_method_unknown")
	case errors.Is(err, authentication.ErrRegistrationReuseHeld):
		kratoswebhook.WriteError(r.Context(), w, http.StatusConflict, "Registration could not be completed.", "registration_unavailable")
	case errors.Is(err, authentication.ErrRegistrationUnavailable):
		kratoswebhook.WriteError(r.Context(), w, http.StatusInternalServerError, "Registration could not be completed.", "registration_unavailable")
	default:
		kratoswebhook.WriteError(r.Context(), w, http.StatusInternalServerError, "Registration could not be completed.", "registration_unavailable")
	}
}

func loginHookFailureResponse(err error) string {
	switch {
	case errors.Is(err, authentication.ErrLoginIdentityLoad):
		return "Failed to get user identity"
	case errors.Is(err, authentication.ErrLoginMemberProvision):
		return "Failed to create member account"
	case errors.Is(err, authentication.ErrLoginMemberValidation):
		return "Failed to validate member account"
	case errors.Is(err, authentication.ErrLoginRoleSynchronization):
		return "Failed to sync user role"
	default:
		return "Failed to validate member account"
	}
}

// recordHookFailure hands a Kratos hook failure to the HTTP request record,
// which logs it once with the response status.
func recordHookFailure(r *http.Request, err error, attrs ...slog.Attr) {
	failurecause.RecorderFrom(r.Context()).Record(failurecause.Of(failurecause.WithAttrs(err, attrs...)))
}

func writeBannedAfterLoginResponse(
	w http.ResponseWriter,
	r *http.Request,
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
		private := []string{}
		if banReason != nil {
			private = append(private, *banReason)
		}
		slog.ErrorContext(r.Context(), "Failed to encode banned response", "failure_cause", failurecause.Message(err, private...))
	}
}
