package authentication

import (
	"context"
	"fmt"
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
		principal, err := recorder.sessions.Principal(request.Context(), sessionID)
		if err == nil && principal != nil && !principal.Banned {
			flowKind, flowErr := recorder.successFlowKind(request.Context(), observation, principal, sessionID)
			if flowErr != nil {
				recordObservedFailure(request.Context(), observation, fmt.Errorf("resolve success flow: %w", flowErr))
				recorder.appendFailure(request.Context(), observation, sharedtelemetry.AuthenticationFailureInternalError)
				writeKratosProxyError(request.Context(), w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
				return
			}
			if err := recorder.appendSuccess(request.Context(), observation, principal, flowKind); err != nil {
				recordObservedFailure(request.Context(), observation, fmt.Errorf("append authentication success: %w", err))
				writeKratosProxyError(request.Context(), w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
				return
			}
			copyBufferedKratosResponse(request.Context(), w, response, nil)
			return
		}
		if err != nil {
			recordObservedFailure(request.Context(), observation, fmt.Errorf("resolve completed principal: %w", err))
		}
		reason := sharedtelemetry.AuthenticationFailureMemberLinkInvalid
		if err == nil && principal != nil && principal.Banned {
			reason = sharedtelemetry.AuthenticationFailureAccountBlocked
		}
		recorder.appendFailure(request.Context(), observation, reason)
		writeKratosProxyError(request.Context(), w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
		return
	}
	// The session lookup runs only for responses below 400, so its failure
	// always replaces the Kratos response.
	if sessionLookupErr != nil {
		recordObservedFailure(request.Context(), observation, fmt.Errorf("resolve terminal session: %w", sessionLookupErr))
		recorder.appendFailure(request.Context(), observation, sharedtelemetry.AuthenticationFailureInternalError)
		writeKratosProxyError(request.Context(), w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
		return
	}

	recorder.appendTerminalDenial(request, observation, response)
	copyBufferedKratosResponse(request.Context(), w, response, nil)
}

// recordObservedFailure gives err to the HTTP request record with the observed
// authentication flow.
func recordObservedFailure(ctx context.Context, observation *authenticationObservation, err error) {
	recordAuthenticationFailure(ctx, failurecause.WithAttrs(err,
		slog.String("flow_kind", string(observation.FlowKind)),
		slog.String("authentication_method", string(observation.Method)),
		slog.String("provider", observation.Provider),
	))
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
		apitelemetry.ReportSecurityAccessAppendFailure(ctx, sharedtelemetry.SecurityAuthenticationSucceeded, sharedtelemetry.AuditAppendFailureActorInvalid)
		return err
	}
	return apitelemetry.BuildAndAppendSecurityAccess(
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
	// The security access writer reports every failed append as a typed audit
	// failure record with its cause, and the denial response does not depend on
	// the append.
	_ = apitelemetry.BuildAndAppendSecurityAccess(
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
}

func (recorder *AuthenticationAccessRecorder) appendBlock(
	ctx context.Context,
	observation *authenticationObservation,
	reason sharedtelemetry.AuthenticationBlockReason,
) {
	// As in appendFailure, the writer reports a failed append itself.
	_ = apitelemetry.BuildAndAppendSecurityAccess(
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
		slog.WarnContext(ctx, "Authentication denial actor rejected; recording anonymous actor", "operation", "build_denial_actor", failurecause.Attr(err))
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
		first, err := recorder.sessions.FirstSession(ctx, sessionID)
		if err != nil {
			return "", err
		}
		if first {
			return sharedtelemetry.AuthenticationFlowRegistration, nil
		}
	}
	return sharedtelemetry.AuthenticationFlowLogin, nil
}
