package account_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/structured"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func credentialHook(f fixture) *account.AccountCredentialHookLifecycle {
	return account.NewAccountCredentialHookLifecycle(f.units, f.kratos, f.audit, f.emails)
}

func googleCredential(email string) auth.Credential {
	provider := structured.Fields{"provider": "google", "subject": "subject"}
	if email != "" {
		provider["email"], provider["email_verified"] = email, true
	}
	return auth.Credential{Type: "oidc", Identifiers: []string{"google:subject"}, Config: structured.Fields{"providers": structured.Values{provider}}}
}

// The durable writer keeps one row per audit ID (internal/telemetry), so a
// replayed hook converges to one record; email.send is at-least-once.
func TestAccountCredentialHookCompletesCommittedOIDCRemoval(t *testing.T) {
	t.Parallel()
	f := newFixture(t, authztest.AllowAll())
	memberID, identityID := f.seedPair("")
	email := identityID + "@example.test"
	previousCredentials := map[string]auth.Credential{
		"oidc": googleCredential(""),
		"code": {Type: "code", Identifiers: []string{email}},
	}
	finalCredentials := map[string]auth.Credential{"code": previousCredentials["code"]}
	auditID := uuid.NewString()
	input := account.AccountCredentialHookInput{
		AuditID: auditID, FlowID: "settings-flow-1", IdentityID: identityID, Kind: account.AccountCredentialOIDC,
		PreviousCredentials: previousCredentials, Credentials: finalCredentials,
		PreviousSnapshotPresent: true, CredentialSnapshotPresent: true,
	}
	lifecycle := credentialHook(f)

	require.NoError(t, lifecycle.Complete(t.Context(), input))
	require.NoError(t, lifecycle.Complete(t.Context(), input), "same Kratos trigger replay must converge")

	records := f.audit.Records()
	require.Len(t, records, 2)
	for _, record := range records {
		require.Equal(t, auditID, record.AuditID)
		require.Equal(t, sharedtelemetry.AuditAccountUpdated, record.Action)
		require.Equal(t, memberID, record.TargetID)
		require.Equal(t, memberID, record.MemberID)
		require.Equal(t, []string{"social_logins"}, record.ChangedFields)
		require.Equal(t, sharedtelemetry.AuditCollectionOperationRemoved, record.CollectionOperation)
		require.Equal(t, "google", record.Provider)
		require.Equal(t, "subject", record.ProviderSubject)
	}
	require.Equal(t, []string{"DeleteIdentitySessions:" + identityID, "DeleteIdentitySessions:" + identityID}, f.kratos.Writes())
	jobs := f.emails.Sent()
	require.Len(t, jobs, 2, "email.send is at-least-once across hook retries")
	require.Equal(t, jobs[0].MessageID, jobs[1].MessageID)
	require.Contains(t, jobs[0].MessageID, auditID)
}

func TestAccountCredentialHookCompletesCommittedPasskeyAddition(t *testing.T) {
	t.Parallel()
	f := newFixture(t, authztest.AllowAll())
	_, identityID := f.seedPair("")
	email := identityID + "@example.test"
	passkey := auth.Credential{Type: "passkey", Config: structured.Fields{"credentials": structured.Values{
		structured.Fields{"id": "passkey-2"}, structured.Fields{"id": "passkey-1"},
	}}}
	f.kratos.Update(identityID, func(identity *auth.Identity) { identity.Credentials["passkey"] = passkey })
	code := auth.Credential{Type: "code", Identifiers: []string{email}}
	auditID := uuid.NewString()

	require.NoError(t, credentialHook(f).Complete(t.Context(), account.AccountCredentialHookInput{
		AuditID: auditID, FlowID: "settings-flow-1", IdentityID: identityID, Kind: account.AccountCredentialPasskey,
		PreviousCredentials:     map[string]auth.Credential{"code": code},
		Credentials:             map[string]auth.Credential{"code": code, "passkey": passkey},
		PreviousSnapshotPresent: true, CredentialSnapshotPresent: true,
	}))

	records := f.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, auditID, records[0].AuditID)
	require.Equal(t, []string{"passkeys"}, records[0].ChangedFields)
	require.Equal(t, sharedtelemetry.AuditCollectionOperationAdded, records[0].CollectionOperation)
	require.Equal(t, []string{"passkey-1", "passkey-2"}, records[0].PasskeyIDs)
	require.Empty(t, f.kratos.Writes())
	jobs := f.emails.Sent()
	require.Len(t, jobs, 1)
	require.Equal(t, "passkey_added", jobs[0].TemplateType)
}

func TestAccountCredentialPreHookRejectsUnrecoverableAndUnbackedPrimary(t *testing.T) {
	t.Parallel()
	f := newFixture(t, authztest.AllowAll())
	_, identityID := f.seedPair("")
	providerEmail := identityID + "@example.test"
	recoveryEmail := "recovery-hook-" + identityID + "@example.test"
	f.kratos.Update(identityID, func(identity *auth.Identity) {
		identity.Credentials = map[string]auth.Credential{
			"oidc": googleCredential(providerEmail),
			"code": {Type: "code", Identifiers: []string{recoveryEmail}},
		}
	})
	lifecycle := credentialHook(f)
	passkeyOnly := map[string]auth.Credential{"passkey": {
		Type: "passkey", Config: structured.Fields{"credentials": structured.Values{structured.Fields{"id": "passkey-1"}}},
	}}

	require.ErrorIs(t, lifecycle.Validate(t.Context(), account.AccountCredentialHookInput{
		FlowID: "settings-flow-1", IdentityID: identityID, Kind: account.AccountCredentialPasskey,
		PreviousCredentials: passkeyOnly, Credentials: passkeyOnly,
		PreviousSnapshotPresent: true, CredentialSnapshotPresent: true,
	}), account.ErrAccountCredentialUnrecoverable)

	err := lifecycle.Validate(t.Context(), account.AccountCredentialHookInput{
		FlowID: "settings-flow-2", IdentityID: identityID, Kind: account.AccountCredentialOIDC,
		PreviousCredentials:     f.kratos.Identity(identityID).Credentials,
		Credentials:             map[string]auth.Credential{"code": {Type: "code", Identifiers: []string{recoveryEmail}}},
		PreviousSnapshotPresent: true, CredentialSnapshotPresent: true,
	})
	require.True(t, errors.Is(err, account.ErrMemberPrimaryEmailUnavailable), err)
	require.True(t, auth.NewCredentialInventory(f.kratos.Identity(identityID).Credentials).HasOIDCProvider("google", "subject"))
	require.Empty(t, f.kratos.Writes())
	require.Empty(t, f.audit.Records())
}
