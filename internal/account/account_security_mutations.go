package account

import (
	"context"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (s *AccountService) SetMyCanonicalEmail(
	ctx context.Context,
	req *connect.Request[managev1.SetMyCanonicalEmailRequest],
) (*connect.Response[managev1.AccountSecurityMutationResponse], error) {
	p, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireFreshSecuritySession(ctx, p.IdentityID.String(), p.SessionID.String()); err != nil {
		return nil, err
	}
	previous, current, changed, err := s.setIdentityCanonicalEmail(ctx, p.IdentityID.String(), req.Msg.Email)
	if err != nil {
		return nil, err
	}
	if changed {
		if err := s.appendRequestAudit(
			ctx,
			sharedtelemetry.AuditAccountUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAccountCanonicalEmailUpdatedAuditRecord(
					metadata, p.MemberID.String(), previous, current,
				)
			},
		); err != nil {
			return nil, errs.Internal(err)
		}
	}
	security, err := s.securityForIdentity(ctx, p.MemberID.String(), p.IdentityID.String(), p.SessionID.String())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.AccountSecurityMutationResponse{Security: security}), nil
}

func (s *AccountService) RequestEmailChange(
	ctx context.Context,
	req *connect.Request[managev1.RequestEmailChangeRequest],
) (*connect.Response[managev1.RequestEmailChangeResponse], error) {
	p, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireFreshSecuritySession(ctx, p.IdentityID.String(), p.SessionID.String()); err != nil {
		return nil, err
	}
	identity, err := s.identity.GetIdentity(ctx, p.IdentityID.String())
	if err != nil || identity == nil {
		return nil, errs.NotFound("identity", p.IdentityID.String())
	}
	normalized, ok := normalizeAccountEmailInput(req.Msg.NewEmail)
	if !ok {
		return connect.NewResponse(&managev1.RequestEmailChangeResponse{Success: false, Message: "Enter a valid email address."}), nil
	}
	if normalized == emailutil.NormalizeAddressForDelivery(identity.CurrentEmail()) {
		return connect.NewResponse(&managev1.RequestEmailChangeResponse{Success: false, Message: "This is already the current email for this account."}), nil
	}
	used, err := emailCodeAddressUsedByAnotherIdentity(ctx, s.identity, p.IdentityID.String(), normalized)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if used {
		return connect.NewResponse(&managev1.RequestEmailChangeResponse{Success: false, Message: "An account with this email already exists."}), nil
	}
	return connect.NewResponse(&managev1.RequestEmailChangeResponse{Success: true, Message: "Email change can continue in account settings."}), nil
}

func (s *AccountService) RevokeMySession(
	ctx context.Context,
	req *connect.Request[managev1.RevokeMySessionRequest],
) (*connect.Response[managev1.AccountSecurityMutationResponse], error) {
	p, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireFreshSecuritySession(ctx, p.IdentityID.String(), p.SessionID.String()); err != nil {
		return nil, err
	}
	if _, err := uuidutil.ParseCanonical(req.Msg.SessionId, "session_id"); err != nil {
		return nil, errs.InvalidArgument("session_id", "must be a canonical UUID")
	}
	if req.Msg.SessionId == p.SessionID.String() {
		return nil, errs.FailedPrecondition("the current session can only be ended through logout")
	}
	var owned bool
	if err := s.db.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM kratos.sessions WHERE id=?::uuid AND identity_id=?::uuid AND active=TRUE AND expires_at>NOW())`, req.Msg.SessionId, p.IdentityID.String()).Scan(&owned).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if !owned {
		return nil, errs.NotFound("session", req.Msg.SessionId)
	}
	if err := s.identity.DeleteSession(ctx, req.Msg.SessionId); err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.appendRequestAudit(
		ctx,
		sharedtelemetry.AuditAccountUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewAccountSessionRevokedAuditRecord(
				metadata,
				p.MemberID.String(),
				sharedtelemetry.AccountSessionScopeOne,
				[]string{req.Msg.SessionId},
			)
		},
	); err != nil {
		return nil, errs.Internal(err)
	}
	security, err := s.securityForIdentity(ctx, p.MemberID.String(), p.IdentityID.String(), p.SessionID.String())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.AccountSecurityMutationResponse{Security: security, SessionsRevoked: true}), nil
}
func (s *AccountService) RevokeMyOtherSessions(
	ctx context.Context,
	_ *connect.Request[managev1.RevokeMyOtherSessionsRequest],
) (*connect.Response[managev1.AccountSecurityMutationResponse], error) {
	p, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireFreshSecuritySession(ctx, p.IdentityID.String(), p.SessionID.String()); err != nil {
		return nil, err
	}
	if p.SessionID == "" {
		return nil, errs.InvalidSession()
	}
	revokedSessionIDs, err := revokeOtherSessions(ctx, s.db, s.identity, p.IdentityID.String(), p.SessionID.String())
	if err != nil {
		return nil, errs.Internal(err)
	}
	if len(revokedSessionIDs) > 0 {
		if err := s.appendRequestAudit(
			ctx,
			sharedtelemetry.AuditAccountUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAccountSessionRevokedAuditRecord(
					metadata,
					p.MemberID.String(),
					sharedtelemetry.AccountSessionScopeOthers,
					revokedSessionIDs,
				)
			},
		); err != nil {
			return nil, errs.Internal(err)
		}
	}
	security, err := s.securityForIdentity(ctx, p.MemberID.String(), p.IdentityID.String(), p.SessionID.String())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.AccountSecurityMutationResponse{Security: security, SessionsRevoked: true}), nil
}

func (s *AccountService) RequestAccountDeletion(
	ctx context.Context,
	_ *connect.Request[managev1.RequestAccountDeletionRequest],
) (*connect.Response[managev1.RequestAccountDeletionResponse], error) {
	p, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireFreshSecuritySession(ctx, p.IdentityID.String(), p.SessionID.String()); err != nil {
		return nil, err
	}
	lifecycle := NewAccountLifecycleService(
		s.db, s.identity, s.spicedb, s.baseURL, s.publisher, WithLifecycleMemberDeletion(s.memberDeletion), WithLifecycleMemberEmailProjection(s.memberEmails),
	)
	if s.auditWriter != nil {
		lifecycle = NewAuditedAccountLifecycleService(
			s.db, s.identity, s.spicedb, s.baseURL, s.publisher, s.auditWriter, WithLifecycleMemberDeletion(s.memberDeletion), WithLifecycleMemberEmailProjection(s.memberEmails),
		)
	}
	result, err := lifecycle.RequestDeletion(ctx, p.MemberID.String(), p.IdentityID.String())
	if err != nil {
		return nil, err
	}
	message := "A confirmation email has been sent. Please check your email to confirm account deletion."
	if result.AlreadyScheduled {
		message = "Account deletion is already scheduled. Check your email for the confirmation link."
	}
	return connect.NewResponse(&managev1.RequestAccountDeletionResponse{Success: true, Message: message}), nil
}
