package authentication

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/auth"
	authenticationdomain "github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/member"
	"gorm.io/gorm"
)

// LoginMemberProvisioner adapts the Member-owned registration/link lifecycle
// to Authentication's application port without exposing Member persistence
// models to the hook handler.
type LoginMemberProvisioner struct {
	members *member.MemberProvisioner
}

func NewLoginMemberProvisioner(members *member.MemberProvisioner) *LoginMemberProvisioner {
	if members == nil {
		panic("Member provisioner is required")
	}
	return &LoginMemberProvisioner{members: members}
}

func (adapter *LoginMemberProvisioner) ProvisionRegistration(
	ctx context.Context,
	input authenticationdomain.LoginMemberInput,
) (string, error) {
	resolved, err := adapter.members.ProvisionRegistration(ctx, member.RegistrationMemberInput{
		IdentityID: input.IdentityID, Email: input.Email, PreferredLocale: input.PreferredLocale,
	})
	if err != nil {
		return "", err
	}
	if resolved == nil {
		return "", errors.New("member provisioning returned no Member")
	}
	return resolved.ID, nil
}

func (adapter *LoginMemberProvisioner) ValidateExistingLink(
	ctx context.Context,
	identity *auth.Identity,
) (string, error) {
	resolved, err := adapter.members.ValidateExistingLink(ctx, identity)
	if errors.Is(err, member.ErrMemberLinkRepairable) {
		return "", errors.Join(authenticationdomain.ErrLoginMemberLinkRepairable, err)
	}
	if err != nil {
		return "", err
	}
	if resolved == nil {
		return "", errors.New("member link validation returned no Member")
	}
	return resolved.ID, nil
}

type RegistrationReuseHoldChecker struct {
	db *gorm.DB
}

func NewRegistrationReuseHoldChecker(db *gorm.DB) *RegistrationReuseHoldChecker {
	if db == nil {
		panic("registration reuse-hold database is required")
	}
	return &RegistrationReuseHoldChecker{db: db}
}

func (adapter *RegistrationReuseHoldChecker) RegistrationEmailReuseBlocked(
	ctx context.Context,
	email string,
) (bool, error) {
	return member.RegistrationEmailReuseBlocked(ctx, adapter.db, email)
}

var (
	_ authenticationdomain.LoginMemberProvisioner       = (*LoginMemberProvisioner)(nil)
	_ authenticationdomain.RegistrationReuseHoldChecker = (*RegistrationReuseHoldChecker)(nil)
)
