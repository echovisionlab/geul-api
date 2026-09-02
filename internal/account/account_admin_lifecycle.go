package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func (s *AccountService) accountBanTargetIdentity(
	ctx context.Context,
	identityManager auth.IdentityGetter,
	identityID string,
) (*auth.Identity, error) {
	identity, err := identityManager.GetIdentity(ctx, identityID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if identity == nil {
		return nil, errs.InternalMsg("target identity is required")
	}
	actor, actorErr := policyv1.NewAccountIdentityActor(identityID)
	if actorErr != nil {
		return nil, errs.Internal(actorErr)
	}
	role, roleErr := globalRoleForActor(ctx, s.spicedb, actor)
	if roleErr != nil {
		return nil, errs.DependencyUnavailable("SpiceDB")
	}
	if role == policyv1.Role.Admin() {
		return nil, errs.FailedPrecondition("admin accounts cannot be banned")
	}
	return identity, nil
}

func (s *AccountService) BanAccount(
	ctx context.Context,
	req *connect.Request[managev1.BanAccountRequest],
) (*connect.Response[managev1.AccountSummary], error) {
	actor, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb)
	if err != nil {
		return nil, err
	}
	if req.Msg.MemberId == actor.MemberID.String() {
		return nil, errs.InvalidArgument("member_id", "cannot ban yourself")
	}
	var until *time.Time
	if req.Msg.Until != nil {
		v := req.Msg.Until.AsTime()
		until = &v
	}
	identityID, err := s.identityIDForMember(ctx, req.Msg.MemberId)
	if err != nil {
		return nil, err
	}
	if err := identitystate.WithMutation(ctx, s.db, identityID, func(
		mutationCtx context.Context,
		tx *gorm.DB,
	) error {
		identity, err := s.accountBanTargetIdentity(mutationCtx, s.identity, identityID)
		if err != nil {
			return err
		}
		if identity.IsBanned() {
			return nil
		}
		if err := NewUserStateService(s.identity).Ban(mutationCtx, identityID, req.Msg.Reason, until); err != nil {
			return err
		}
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendRequest(
			mutationCtx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditMemberUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberBannedAuditRecord(metadata, req.Msg.MemberId)
			},
		)
	}); err != nil {
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			return nil, connectErr
		}
		return nil, errs.Wrap(err)
	}
	return s.accountSummaryForAdmin(ctx, req.Msg.MemberId)
}
func (s *AccountService) UnbanAccount(
	ctx context.Context,
	req *connect.Request[managev1.UnbanAccountRequest],
) (*connect.Response[managev1.AccountSummary], error) {
	if _, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb); err != nil {
		return nil, err
	}
	identityID, err := s.identityIDForMember(ctx, req.Msg.MemberId)
	if err != nil {
		return nil, err
	}
	if _, err := clearUserBan(ctx, s.db, s.identity, identityID, func(mutationCtx context.Context, tx *gorm.DB) error {
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendRequest(
			mutationCtx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditMemberUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberUnbannedAuditRecord(metadata, req.Msg.MemberId)
			},
		)
	}); err != nil {
		return nil, err
	}
	return s.accountSummaryForAdmin(ctx, req.Msg.MemberId)
}
func (s *AccountService) SetAccountCanonicalEmail(
	ctx context.Context,
	req *connect.Request[managev1.SetAccountCanonicalEmailRequest],
) (*connect.Response[managev1.AccountSummary], error) {
	if _, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb); err != nil {
		return nil, err
	}
	identityID, err := s.identityIDForMember(ctx, req.Msg.MemberId)
	if err != nil {
		return nil, err
	}
	previous, current, changed, err := s.setIdentityCanonicalEmail(ctx, identityID, req.Msg.Email)
	if err != nil {
		return nil, err
	}
	if changed {
		if err := s.appendRequestAudit(
			ctx,
			sharedtelemetry.AuditAccountUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAccountCanonicalEmailUpdatedAuditRecord(
					metadata, req.Msg.MemberId, previous, current,
				)
			},
		); err != nil {
			return nil, errs.Internal(err)
		}
	}
	return s.accountSummaryForAdmin(ctx, req.Msg.MemberId)
}
func (s *AccountService) RemoveAccountSsoProvider(
	ctx context.Context,
	req *connect.Request[managev1.RemoveAccountSsoProviderRequest],
) (*connect.Response[managev1.AccountSummary], error) {
	actor, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb)
	if err != nil {
		return nil, err
	}
	if req.Msg.MemberId == actor.MemberID.String() {
		return nil, errs.InvalidArgument("member_id", "cannot remove your own SSO provider")
	}
	identityID, err := s.identityIDForMember(ctx, req.Msg.MemberId)
	if err != nil {
		return nil, err
	}
	provider := auth.NormalizeOIDCProvider(req.Msg.Provider)
	providerSubject := strings.TrimSpace(req.Msg.Identifier)
	identity, err := s.identity.GetIdentityWithIncludeCredential(ctx, identityID, "oidc")
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("load account social credential: %w", err))
	}
	if identity == nil {
		return nil, errs.Internal(fmt.Errorf("load account social credential: identity is missing"))
	}
	hadCredential := auth.NewCredentialInventory(identity.Credentials).HasOIDCProvider(provider, providerSubject)
	if err := s.credentialMutator.RemoveOIDCProvider(ctx, identityID, provider, providerSubject); err != nil {
		return nil, err
	}
	if hadCredential {
		if err := s.appendRequestAudit(
			ctx,
			sharedtelemetry.AuditAccountUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAccountSocialLoginRemovedAuditRecord(
					metadata, req.Msg.MemberId, provider, providerSubject,
				)
			},
		); err != nil {
			return nil, errs.Internal(err)
		}
	}
	return s.accountSummaryForAdmin(ctx, req.Msg.MemberId)
}
func (s *AccountService) DeleteAccount(
	ctx context.Context,
	req *connect.Request[managev1.DeleteAccountRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if s.memberDeletion == nil {
		return nil, errs.InternalMsg("Member deletion lifecycle is unavailable")
	}
	actor, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb)
	if err != nil {
		return nil, err
	}
	if req.Msg.MemberId == actor.MemberID.String() {
		return nil, errs.InvalidArgument("member_id", "cannot delete yourself")
	}
	if _, err := uuidutil.ParseCanonical(req.Msg.MemberId, "member_id"); err != nil {
		return nil, errs.InvalidArgument("member_id", "must be a canonical UUID")
	}
	identityPublisher, publisherOK := any(s.publisher).(UserDeletionIdentityDispatchPublisher)
	if !publisherOK {
		return nil, errs.Internal(fmt.Errorf("unonboarded Member deletion publisher is not configured"))
	}
	accepted, err := EnqueueUnonboardedMemberHardDelete(
		ctx, s.db, identityPublisher, s.memberDeletion, s.auditWriter, req.Msg.MemberId,
	)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if accepted {
		return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
	}
	target, err := authorizationtarget.RequireLinkedMember(ctx, s.db, req.Msg.MemberId, true)
	if err != nil {
		return nil, err
	}
	if err := scheduleImmediateUserDeletion(
		ctx,
		s.db,
		s.identity,
		s.publisher,
		s.spicedb,
		s.memberDeletion,
		req.Msg.MemberId,
		target.IdentityID,
		func(mutationCtx context.Context, tx *gorm.DB, previousState string) error {
			if s.auditWriter == nil {
				return nil
			}
			return domainaudit.AppendRequest(
				mutationCtx,
				tx,
				s.auditWriter,
				sharedtelemetry.AuditAccountUpdated,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewAccountDeletionScheduledAuditRecord(
						metadata, req.Msg.MemberId, sharedtelemetry.AuditState(previousState),
					)
				},
			)
		},
		s.memberEmails,
	); err != nil {
		if errors.Is(err, ErrLastActiveAdminDeletion) {
			return nil, errs.FailedPrecondition("the last active admin cannot be deleted")
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}
