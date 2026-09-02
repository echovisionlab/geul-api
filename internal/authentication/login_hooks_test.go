package authentication

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

const (
	loginHookIdentityID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	loginHookMemberID   = "bbbbbbbb-bbbb-4bbb-9bbb-bbbbbbbbbbbb"
)

type loginHookIdentityReader struct {
	identity *auth.Identity
	err      error
}

func (reader loginHookIdentityReader) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return reader.identity, reader.err
}

type loginHookMemberProvisioner struct {
	provisioned  []LoginMemberInput
	validated    []*auth.Identity
	memberID     string
	validateErr  error
	provisionErr error
}

func (provisioner *loginHookMemberProvisioner) ProvisionRegistration(
	_ context.Context,
	input LoginMemberInput,
) (string, error) {
	provisioner.provisioned = append(provisioner.provisioned, input)
	return provisioner.memberID, provisioner.provisionErr
}

func (provisioner *loginHookMemberProvisioner) ValidateExistingLink(
	_ context.Context,
	identity *auth.Identity,
) (string, error) {
	provisioner.validated = append(provisioner.validated, identity)
	return provisioner.memberID, provisioner.validateErr
}

type loginHookRoleSynchronizer struct {
	identityID string
	memberID   string
	err        error
}

func (synchronizer *loginHookRoleSynchronizer) EnsureLoginRole(
	_ context.Context,
	identityID string,
	memberID string,
) (bool, error) {
	synchronizer.identityID, synchronizer.memberID = identityID, memberID
	return false, synchronizer.err
}

func TestLoginHookBannedIdentityHasNoMemberOrRoleSideEffects(t *testing.T) {
	reason := "policy"
	members := &loginHookMemberProvisioner{memberID: loginHookMemberID}
	roles := &loginHookRoleSynchronizer{}
	service := NewLoginHookService(loginHookIdentityReader{identity: &auth.Identity{
		ID: loginHookIdentityID, State: auth.KratosStateInactive,
		Traits:        structured.Fields{"email": "banned@example.test"},
		MetadataAdmin: structured.Fields{"banned": true, "ban_reason": reason},
	}}, members, roles)

	result, err := service.Process(t.Context(), LoginHookInput{
		IdentityID: loginHookIdentityID, Email: "banned@example.test", Trigger: "registration",
	})

	require.NoError(t, err)
	require.True(t, result.Banned)
	require.Equal(t, reason, *result.BanReason)
	require.Empty(t, members.provisioned)
	require.Empty(t, roles.memberID)
}

func TestLoginHookRegistrationProvisionsMemberThenSynchronizesRole(t *testing.T) {
	members := &loginHookMemberProvisioner{memberID: loginHookMemberID}
	roles := &loginHookRoleSynchronizer{}
	service := NewLoginHookService(loginHookIdentityReader{identity: &auth.Identity{
		ID: loginHookIdentityID, Traits: structured.Fields{"email": "member@example.test"},
	}}, members, roles)

	result, err := service.Process(t.Context(), LoginHookInput{
		IdentityID: loginHookIdentityID, Email: "member@example.test",
		PreferredLocale: "ko", Trigger: " Registration ",
	})

	require.NoError(t, err)
	require.True(t, result.NewUser)
	require.Equal(t, loginHookMemberID, result.MemberID)
	require.Equal(t, []LoginMemberInput{{
		IdentityID: loginHookIdentityID, Email: "member@example.test", PreferredLocale: "ko",
	}}, members.provisioned)
	require.Equal(t, loginHookIdentityID, roles.identityID)
	require.Equal(t, loginHookMemberID, roles.memberID)
}

func TestLoginHookRepairsOnlyExplicitlyRepairableMemberLink(t *testing.T) {
	identity := &auth.Identity{
		ID: loginHookIdentityID, Traits: structured.Fields{"email": "member@example.test"},
	}
	members := &loginHookMemberProvisioner{
		memberID: loginHookMemberID, validateErr: ErrLoginMemberLinkRepairable,
	}
	service := NewLoginHookService(
		loginHookIdentityReader{identity: identity}, members, &loginHookRoleSynchronizer{},
	)

	result, err := service.Process(t.Context(), LoginHookInput{IdentityID: loginHookIdentityID})

	require.NoError(t, err)
	require.Equal(t, loginHookMemberID, result.MemberID)
	require.Equal(t, []LoginMemberInput{{
		IdentityID: loginHookIdentityID, Email: "member@example.test", PreferredLocale: "en",
	}}, members.provisioned)
}

type registrationHoldChecker struct {
	blocked bool
	err     error
}

func (checker registrationHoldChecker) RegistrationEmailReuseBlocked(context.Context, string) (bool, error) {
	return checker.blocked, checker.err
}

func TestRegistrationHookPolicyOwnsMethodAndReuseHoldRules(t *testing.T) {
	tests := []struct {
		name    string
		checker registrationHoldChecker
		input   RegistrationHookInput
		wantErr error
	}{
		{name: "code", input: RegistrationHookInput{Method: "code"}},
		{name: "oidc", input: RegistrationHookInput{Method: " OIDC "}},
		{name: "pending email", input: RegistrationHookInput{Method: "code", PendingEmail: "new@example.test"}, wantErr: ErrRegistrationPendingEmail},
		{name: "passkey", input: RegistrationHookInput{Method: "passkey"}, wantErr: ErrRegistrationMethodDenied},
		{name: "unknown", input: RegistrationHookInput{Method: "password"}, wantErr: ErrRegistrationMethodUnknown},
		{name: "reuse held", checker: registrationHoldChecker{blocked: true}, input: RegistrationHookInput{Method: "code"}, wantErr: ErrRegistrationReuseHeld},
		{name: "lookup failure", checker: registrationHoldChecker{err: errors.New("database unavailable")}, input: RegistrationHookInput{Method: "oidc"}, wantErr: ErrRegistrationUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewRegistrationHookPolicy(tt.checker).Validate(t.Context(), tt.input)
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}
