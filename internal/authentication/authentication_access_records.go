package authentication

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (recorder *AuthenticationAccessRecorder) complete(
	w http.ResponseWriter,
	request *http.Request,
	observation *authenticationObservation,
	response bufferedKratosResponse,
) {
	if observation.OIDCCallback {
		observation.IncomingMember = recorder.resolveIncoming(request)
	}

	sessionID, sessionLookupErr := recorder.sessionIDFromTerminalResponse(request, observation, response)
	if sessionID != "" {
		principal, err := recorder.resolvePrincipal(request.Context(), sessionID)
		if err == nil && principal != nil && !principal.Banned {
			flowKind, flowErr := recorder.successFlowKind(request.Context(), observation, principal, sessionID)
			if flowErr == nil && recorder.appendSuccess(request.Context(), observation, principal, flowKind) == nil {
				copyBufferedKratosResponse(request.Context(), w, response, nil)
				return
			}
			if flowErr != nil {
				slog.ErrorContext(request.Context(), "Operation failed", "reason", "resolve_success_flow", "status_code", http.StatusServiceUnavailable, failurecause.Attr(flowErr), "flow_kind", string(observation.FlowKind), "authentication_method", string(observation.Method), "provider", observation.Provider)
				recorder.appendFailure(request.Context(), observation, sharedtelemetry.AuthenticationFailureInternalError)
			}
			writeKratosProxyError(request.Context(), w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
			return
		}
		if err != nil {
			level := slog.LevelError
			if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
				level = slog.LevelWarn
			}
			slog.Log(request.Context(), level, "Operation failed", "reason", "resolve_completed_principal", "status_code", http.StatusServiceUnavailable, failurecause.Attr(err), "flow_kind", string(observation.FlowKind), "authentication_method", string(observation.Method), "provider", observation.Provider)
		}
		reason := sharedtelemetry.AuthenticationFailureMemberLinkInvalid
		if err == nil && principal != nil && principal.Banned {
			reason = sharedtelemetry.AuthenticationFailureAccountBlocked
		}
		recorder.appendFailure(request.Context(), observation, reason)
		writeKratosProxyError(request.Context(), w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
		return
	}
	if sessionLookupErr != nil {
		slog.ErrorContext(request.Context(), "Operation failed", "reason", "resolve_terminal_session", failurecause.Attr(sessionLookupErr), "flow_kind", string(observation.FlowKind), "authentication_method", string(observation.Method), "provider", observation.Provider)
	}
	if sessionLookupErr != nil && response.StatusCode < http.StatusBadRequest {
		recorder.appendFailure(request.Context(), observation, sharedtelemetry.AuthenticationFailureInternalError)
		writeKratosProxyError(request.Context(), w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
		return
	}

	recorder.appendTerminalDenial(request, observation, response)
	copyBufferedKratosResponse(request.Context(), w, response, nil)
}

func (recorder *AuthenticationAccessRecorder) appendSuccess(
	ctx context.Context,
	observation *authenticationObservation,
	principal *auth.UserInfo,
	flowKind sharedtelemetry.AuthenticationFlowKind,
) error {
	actor, err := sharedtelemetry.ActorForRecord(sharedtelemetry.MemberActor{
		SessionID: principal.SessionID.String(), IdentityID: principal.IdentityID.String(), MemberID: principal.MemberID.String(),
	})
	if err != nil {
		slog.ErrorContext(ctx, "Operation failed", "reason", "build_success_actor", "status_code", http.StatusServiceUnavailable, failurecause.Attr(err), "flow_kind", string(observation.FlowKind), "authentication_method", string(observation.Method), "provider", observation.Provider)
		apitelemetry.ReportSecurityAccessAppendFailure(ctx, sharedtelemetry.SecurityAuthenticationSucceeded, sharedtelemetry.AuditAppendFailureActorInvalid)
		return err
	}
	err = apitelemetry.BuildAndAppendSecurityAccess(
		ctx,
		recorder.writer,
		sharedtelemetry.SecurityAuthenticationSucceeded,
		actor,
		recorder.now(),
		func(metadata sharedtelemetry.SecurityAccessMetadata) (sharedtelemetry.SecurityAccessRecord, error) {
			principalState := sharedtelemetry.AuthenticationPrincipalOnboardingOnly
			if principal.Onboarded {
				principalState = sharedtelemetry.AuthenticationPrincipalActive
			}
			return sharedtelemetry.NewAuthenticationSucceededRecord(metadata, sharedtelemetry.AuthenticationContext{
				FlowKind: flowKind, AuthenticationMethod: observation.Method, PrincipalState: principalState, Provider: observation.Provider,
			})
		},
	)
	if err != nil {
		slog.ErrorContext(ctx, "Operation failed", "reason", "append_authentication_success", "status_code", http.StatusServiceUnavailable, failurecause.Attr(err), "flow_kind", string(observation.FlowKind), "authentication_method", string(observation.Method), "provider", observation.Provider)
	}
	return err
}

func (recorder *AuthenticationAccessRecorder) appendFailure(
	ctx context.Context,
	observation *authenticationObservation,
	reason sharedtelemetry.AuthenticationFailureReason,
) {
	if observation.FlowKind == "" || observation.Method == "" {
		recorder.appendBlock(ctx, observation, sharedtelemetry.AuthenticationBlockRequestInvalid)
		return
	}
	err := apitelemetry.BuildAndAppendSecurityAccess(
		ctx,
		recorder.writer,
		sharedtelemetry.SecurityAuthenticationFailed,
		recorder.denialActor(ctx, observation),
		recorder.now(),
		func(metadata sharedtelemetry.SecurityAccessMetadata) (sharedtelemetry.SecurityAccessRecord, error) {
			return sharedtelemetry.NewAuthenticationFailedRecord(metadata, sharedtelemetry.AuthenticationContext{
				FlowKind: observation.FlowKind, AuthenticationMethod: observation.Method, Provider: observation.Provider,
			}, reason)
		},
	)
	if err != nil {
		slog.ErrorContext(ctx, "Operation failed", "reason", "append_authentication_failure", failurecause.Attr(err), "authentication_failure_reason", string(reason), "flow_kind", string(observation.FlowKind), "authentication_method", string(observation.Method), "provider", observation.Provider)
	}
}

func (recorder *AuthenticationAccessRecorder) appendBlock(
	ctx context.Context,
	observation *authenticationObservation,
	reason sharedtelemetry.AuthenticationBlockReason,
) {
	err := apitelemetry.BuildAndAppendSecurityAccess(
		ctx,
		recorder.writer,
		sharedtelemetry.SecurityAuthenticationBlocked,
		recorder.denialActor(ctx, observation),
		recorder.now(),
		func(metadata sharedtelemetry.SecurityAccessMetadata) (sharedtelemetry.SecurityAccessRecord, error) {
			return sharedtelemetry.NewAuthenticationBlockedRecord(metadata, sharedtelemetry.AuthenticationContext{
				FlowKind: observation.FlowKind, AuthenticationMethod: observation.Method, Provider: observation.Provider,
			}, reason)
		},
	)
	if err != nil {
		slog.ErrorContext(ctx, "Operation failed", "reason", "append_authentication_block", failurecause.Attr(err), "authentication_block_reason", string(reason), "flow_kind", string(observation.FlowKind), "authentication_method", string(observation.Method), "provider", observation.Provider)
	}
}

func (recorder *AuthenticationAccessRecorder) appendTerminalDenial(
	request *http.Request,
	observation *authenticationObservation,
	response bufferedKratosResponse,
) {
	if observation.FacadeBlock != "" {
		recorder.appendBlock(request.Context(), observation, observation.FacadeBlock)
		return
	}
	errorID := kratosErrorID(response.Body)
	switch errorID {
	case "security_csrf_violation", "security_identity_mismatch":
		recorder.appendBlock(request.Context(), observation, sharedtelemetry.AuthenticationBlockIntegrityCheckFailed)
		return
	case "self_service_flow_expired", "self_service_flow_replaced", "self_service_flow_disabled", "self_service_flow_return_to_forbidden":
		recorder.appendBlock(request.Context(), observation, sharedtelemetry.AuthenticationBlockFlowInvalid)
		return
	}
	if response.StatusCode == http.StatusTooManyRequests {
		recorder.appendBlock(request.Context(), observation, sharedtelemetry.AuthenticationBlockRateLimited)
		return
	}
	if jsonBodyContains(response.Body, "reason", "account_banned") {
		recorder.appendFailure(request.Context(), observation, sharedtelemetry.AuthenticationFailureAccountBlocked)
		return
	}
	if !observation.ProofSubmitted {
		reason := sharedtelemetry.AuthenticationBlockRequestInvalid
		if response.StatusCode >= http.StatusInternalServerError {
			reason = sharedtelemetry.AuthenticationBlockServiceUnavailable
		}
		recorder.appendBlock(request.Context(), observation, reason)
		return
	}
	if observation.Method == sharedtelemetry.AuthenticationMethodOIDC {
		if strings.EqualFold(request.URL.Query().Get("error"), "access_denied") {
			recorder.appendFailure(request.Context(), observation, sharedtelemetry.AuthenticationFailureProviderDenied)
			return
		}
		recorder.appendFailure(request.Context(), observation, sharedtelemetry.AuthenticationFailureProviderFailed)
		return
	}
	recorder.appendFailure(request.Context(), observation, sharedtelemetry.AuthenticationFailureProofRejected)
}

func (recorder *AuthenticationAccessRecorder) denialActor(ctx context.Context, observation *authenticationObservation) sharedtelemetry.RecordActor {
	if observation != nil && observation.FlowKind == sharedtelemetry.AuthenticationFlowReauthentication && observation.IncomingMember != nil {
		actor, err := sharedtelemetry.ActorForRecord(sharedtelemetry.MemberActor{MemberID: observation.IncomingMember.MemberID.String()})
		if err == nil {
			return actor
		}
		slog.ErrorContext(ctx, "Build authentication denial actor failed", failurecause.Attr(err))
	}
	actor, _ := sharedtelemetry.ActorForRecord(sharedtelemetry.AnonymousActor{})
	return actor
}

func (recorder *AuthenticationAccessRecorder) successFlowKind(
	ctx context.Context,
	observation *authenticationObservation,
	principal *auth.UserInfo,
	sessionID string,
) (sharedtelemetry.AuthenticationFlowKind, error) {
	if observation.FlowKind == sharedtelemetry.AuthenticationFlowRegistration ||
		observation.FlowKind == sharedtelemetry.AuthenticationFlowReauthentication {
		return observation.FlowKind, nil
	}
	if observation.OIDCCallback && observation.IncomingMember != nil &&
		observation.IncomingMember.MemberID == principal.MemberID {
		return sharedtelemetry.AuthenticationFlowReauthentication, nil
	}
	if observation.OIDCCallback {
		first, err := recorder.firstSession(ctx, sessionID)
		if err != nil {
			return "", err
		}
		if first {
			return sharedtelemetry.AuthenticationFlowRegistration, nil
		}
	}
	return sharedtelemetry.AuthenticationFlowLogin, nil
}
