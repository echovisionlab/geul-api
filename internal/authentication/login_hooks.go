package authentication

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/localization"
)

var (
	ErrLoginHookInput            = errors.New("login hook input is invalid")
	ErrLoginIdentityLoad         = errors.New("login identity could not be loaded")
	ErrLoginMemberProvision      = errors.New("login Member could not be provisioned")
	ErrLoginMemberValidation     = errors.New("login Member link could not be validated")
	ErrLoginMemberLinkRepairable = errors.New("login Member link is repairable")
	ErrLoginRoleSynchronization  = errors.New("login role could not be synchronized")
	ErrRegistrationPendingEmail  = errors.New("pending account email is forbidden during registration")
	ErrRegistrationMethodDenied  = errors.New("registration method is denied")
	ErrRegistrationMethodUnknown = errors.New("registration method is unknown")
	ErrRegistrationReuseHeld     = errors.New("registration email is in a reuse hold")
	ErrRegistrationUnavailable   = errors.New("registration is unavailable")
)

type LoginIdentityReader interface {
	GetIdentity(context.Context, string) (*auth.Identity, error)
}

type LoginMemberInput struct {
	IdentityID      string
	Email           string
	PreferredLocale string
}

type LoginMemberProvisioner interface {
	ProvisionRegistration(context.Context, LoginMemberInput) (string, error)
	ValidateExistingLink(context.Context, *auth.Identity) (string, error)
}

type LoginRoleSynchronizer interface {
	EnsureLoginRole(context.Context, string, string) (bool, error)
}

type LoginHookInput struct {
	IdentityID      string
	Email           string
	PreferredLocale string
	Trigger         string
}

type LoginHookResult struct {
	Banned    bool
	BanReason *string
	NewUser   bool
	MemberID  string
}

// LoginHookService owns the account/member convergence performed after Kratos
// has completed authentication. HTTP handlers only translate its result.
type LoginHookService struct {
	identities LoginIdentityReader
	members    LoginMemberProvisioner
	roles      LoginRoleSynchronizer
}

func NewLoginHookService(
	identities LoginIdentityReader,
	members LoginMemberProvisioner,
	roles LoginRoleSynchronizer,
) *LoginHookService {
	if identities == nil || members == nil || roles == nil {
		panic("login identity, Member provisioning, and role synchronization ports are required")
	}
	return &LoginHookService{identities: identities, members: members, roles: roles}
}

func (service *LoginHookService) Process(
	ctx context.Context,
	input LoginHookInput,
) (LoginHookResult, error) {
	result := LoginHookResult{NewUser: isRegistrationTrigger(input.Trigger)}
	if strings.TrimSpace(input.IdentityID) == "" {
		return result, ErrLoginHookInput
	}

	identity, err := service.identities.GetIdentity(ctx, input.IdentityID)
	if err != nil || identity == nil {
		if err == nil {
			err = fmt.Errorf("identity %s was not returned", input.IdentityID)
		}
		return result, errors.Join(ErrLoginIdentityLoad, err)
	}
	result.Banned = identity.IsBanned()
	result.BanReason = identity.GetBanReason()
	if result.Banned {
		return result, nil
	}

	memberID, err := service.resolveMember(ctx, input, identity, result.NewUser)
	if err == nil && strings.TrimSpace(memberID) == "" {
		err = errors.New("login Member ID is missing")
	}
	if err != nil {
		if result.NewUser {
			return result, errors.Join(ErrLoginMemberProvision, err)
		}
		return result, errors.Join(ErrLoginMemberValidation, err)
	}
	result.MemberID = memberID
	if _, err := service.roles.EnsureLoginRole(ctx, input.IdentityID, memberID); err != nil {
		return result, errors.Join(ErrLoginRoleSynchronization, err)
	}
	return result, nil
}

func (service *LoginHookService) resolveMember(
	ctx context.Context,
	input LoginHookInput,
	identity *auth.Identity,
	registration bool,
) (string, error) {
	if registration {
		return service.members.ProvisionRegistration(ctx, LoginMemberInput{
			IdentityID: input.IdentityID, Email: input.Email, PreferredLocale: input.PreferredLocale,
		})
	}
	memberID, err := service.members.ValidateExistingLink(ctx, identity)
	if err == nil || !errors.Is(err, ErrLoginMemberLinkRepairable) {
		return memberID, err
	}
	preferredLocale := strings.TrimSpace(input.PreferredLocale)
	if preferredLocale == "" {
		preferredLocale = localization.LocaleEnglish
	}
	return service.members.ProvisionRegistration(ctx, LoginMemberInput{
		IdentityID: input.IdentityID, Email: identity.CurrentEmail(), PreferredLocale: preferredLocale,
	})
}

func isRegistrationTrigger(trigger string) bool {
	return strings.EqualFold(strings.TrimSpace(trigger), "registration")
}

type RegistrationReuseHoldChecker interface {
	RegistrationEmailReuseBlocked(context.Context, string) (bool, error)
}

type RegistrationHookInput struct {
	Email        string
	PendingEmail string
	Method       string
}

type RegistrationHookPolicy struct {
	reuseHolds RegistrationReuseHoldChecker
}

func NewRegistrationHookPolicy(reuseHolds RegistrationReuseHoldChecker) *RegistrationHookPolicy {
	if reuseHolds == nil {
		panic("registration email reuse hold checker is required")
	}
	return &RegistrationHookPolicy{reuseHolds: reuseHolds}
}

func (policy *RegistrationHookPolicy) Validate(ctx context.Context, input RegistrationHookInput) error {
	if strings.TrimSpace(input.PendingEmail) != "" {
		return ErrRegistrationPendingEmail
	}
	switch strings.ToLower(strings.TrimSpace(input.Method)) {
	case "code", "oidc":
		blocked, err := policy.reuseHolds.RegistrationEmailReuseBlocked(ctx, input.Email)
		if err != nil {
			return errors.Join(ErrRegistrationUnavailable, err)
		}
		if blocked {
			return ErrRegistrationReuseHeld
		}
		return nil
	case "passkey":
		return ErrRegistrationMethodDenied
	default:
		return ErrRegistrationMethodUnknown
	}
}
