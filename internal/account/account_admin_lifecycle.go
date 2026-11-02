package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// BanAccountCommand bans another Member's Identity, until Until when set.
type BanAccountCommand struct {
	MemberID string
	Reason   *string
	Until    *time.Time
}

// UnbanAccountCommand lifts a Member's ban.
type UnbanAccountCommand struct{ MemberID string }

// SetAccountCanonicalEmailCommand makes a proven address a Member's
// canonical account email.
type SetAccountCanonicalEmailCommand struct {
	MemberID string
	Email    string
}

// RemoveAccountSsoProviderCommand unlinks one social login of another Member.
type RemoveAccountSsoProviderCommand struct {
	MemberID   string
	Provider   string
	Identifier string
}

// DeleteAccountCommand deletes another Member's account.
type DeleteAccountCommand struct{ MemberID string }

func (s *Service) accountBanTargetIdentity(ctx context.Context, identityID string) (*auth.Identity, error) {
	identity, err := s.identity.GetIdentity(ctx, identityID)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, errors.New("target identity is required")
	}
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	if err != nil {
		return nil, err
	}
	role, err := globalRoleForActor(ctx, s.spicedb, actor)
	if err != nil {
		return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if role == policyv1.Role.Admin() {
		return nil, domainerr.Precondition("admin accounts cannot be banned")
	}
	return identity, nil
}

// BanAccount deactivates another non-administrator Identity and revokes its
// sessions. Banning a banned Identity changes nothing.
func (s *Service) BanAccount(ctx context.Context, command BanAccountCommand) (*Summary, error) {
	actor, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb)
	if err != nil {
		return nil, err
	}
	if command.MemberID == actor.MemberID.String() {
		return nil, domainerr.InvalidField("member_id", "cannot ban yourself")
	}
	identityID, err := s.identityIDForMember(ctx, command.MemberID)
	if err != nil {
		return nil, err
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[bool, Store]{
		Subjects: []string{identityID},
		Access:   freshAdmin(),
		Change: func(ctx context.Context, _ Store) (bool, error) {
			identity, err := s.accountBanTargetIdentity(ctx, identityID)
			if err != nil || identity.IsBanned() {
				return false, err
			}
			return true, NewUserStateService(s.identity).Ban(ctx, identityID, command.Reason, command.Until)
		},
		Audit: func(banned bool) mutation.Audit {
			if !banned {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditMemberUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberBannedAuditRecord(metadata, command.MemberID)
			})
		},
	}); err != nil {
		return nil, err
	}
	return s.adminAccountSummary(ctx, command.MemberID)
}

// UnbanAccount reactivates a banned Identity whose account is not pending
// deletion.
func (s *Service) UnbanAccount(ctx context.Context, command UnbanAccountCommand) (*Summary, error) {
	if _, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb); err != nil {
		return nil, err
	}
	identityID, err := s.identityIDForMember(ctx, command.MemberID)
	if err != nil {
		return nil, err
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[bool, Store]{
		Subjects: []string{identityID},
		Access:   freshAdmin(),
		Change: func(ctx context.Context, store Store) (bool, error) {
			return clearUserBan(ctx, store, s.identity, identityID)
		},
		Audit: func(cleared bool) mutation.Audit {
			if !cleared {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditMemberUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberUnbannedAuditRecord(metadata, command.MemberID)
			})
		},
	}); err != nil {
		if errors.Is(err, ErrPendingAccountDeletion) {
			return nil, domainerr.Precondition("an account pending deletion cannot be unbanned").WithCause(err)
		}
		return nil, err
	}
	return s.adminAccountSummary(ctx, command.MemberID)
}

// requireFreshAdmin applies the administrator identity fence before a
// mutation whose Identity fence the email or credential lifecycle holds
// itself. That lifecycle writes the Kratos Identity, which may be the
// administrator's own, so the check cannot share its transaction.
func (s *Service) requireFreshAdmin(ctx context.Context) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Access: freshAdmin(),
		Change: func(context.Context, Store) (struct{}, error) { return struct{}{}, nil },
		Audit:  func(struct{}) mutation.Audit { return mutation.Unchanged() },
	})
	return err
}

// SetAccountCanonicalEmail makes a proven address the canonical email of a
// Member's account.
func (s *Service) SetAccountCanonicalEmail(ctx context.Context, command SetAccountCanonicalEmailCommand) (*Summary, error) {
	if _, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb); err != nil {
		return nil, err
	}
	identityID, err := s.identityIDForMember(ctx, command.MemberID)
	if err != nil {
		return nil, err
	}
	if err := s.requireFreshAdmin(ctx); err != nil {
		return nil, err
	}
	if err := s.changeCanonicalEmail(ctx, command.MemberID, identityID, command.Email); err != nil {
		return nil, err
	}
	return s.adminAccountSummary(ctx, command.MemberID)
}

// RemoveAccountSsoProvider unlinks one social login of another Member.
func (s *Service) RemoveAccountSsoProvider(ctx context.Context, command RemoveAccountSsoProviderCommand) (*Summary, error) {
	actor, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb)
	if err != nil {
		return nil, err
	}
	if command.MemberID == actor.MemberID.String() {
		return nil, domainerr.InvalidField("member_id", "cannot remove your own SSO provider")
	}
	identityID, err := s.identityIDForMember(ctx, command.MemberID)
	if err != nil {
		return nil, err
	}
	if err := s.requireFreshAdmin(ctx); err != nil {
		return nil, err
	}
	provider := auth.NormalizeOIDCProvider(command.Provider)
	providerSubject := strings.TrimSpace(command.Identifier)
	identity, err := s.identity.GetIdentityWithIncludeCredential(ctx, identityID, "oidc")
	if err != nil {
		return nil, fmt.Errorf("load account social credential: %w", err)
	}
	if identity == nil {
		return nil, errors.New("load account social credential: identity is missing")
	}
	hadCredential := auth.NewCredentialInventory(identity.Credentials).HasOIDCProvider(provider, providerSubject)
	if err := s.credentials().RemoveOIDCProvider(ctx, identityID, provider, providerSubject); err != nil {
		return nil, failurecause.WithAttrs(err, slog.String("identity_id", identityID), slog.String("provider", provider))
	}
	if hadCredential {
		if err := s.appendAccountUpdatedAudit(ctx, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewAccountSocialLoginRemovedAuditRecord(metadata, command.MemberID, provider, providerSubject)
		}); err != nil {
			return nil, err
		}
	}
	return s.adminAccountSummary(ctx, command.MemberID)
}

// DeleteAccount deletes another Member's account: an unonboarded Member is
// hard-deleted by the worker, an onboarded one is scheduled for immediate
// tombstoning.
func (s *Service) DeleteAccount(ctx context.Context, command DeleteAccountCommand) error {
	actor, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb)
	if err != nil {
		return err
	}
	if command.MemberID == actor.MemberID.String() {
		return domainerr.InvalidField("member_id", "cannot delete yourself")
	}
	if _, err := uuidutil.ParseCanonical(command.MemberID, "member_id"); err != nil {
		return domainerr.InvalidField("member_id", "must be a canonical UUID")
	}
	accepted, err := s.enqueueUnonboardedHardDelete(ctx, command.MemberID)
	if err != nil || accepted {
		return err
	}
	var target Target
	if err := s.read(ctx, func(store Store) error {
		var err error
		target, err = store.RequireLinkedMember(ctx, command.MemberID, true)
		return err
	}); err != nil {
		return err
	}
	if err := s.scheduleImmediateDeletion(ctx, command.MemberID, target.IdentityID); err != nil {
		if errors.Is(err, ErrLastActiveAdminDeletion) {
			return domainerr.Precondition("the last active admin cannot be deleted")
		}
		return err
	}
	return nil
}
