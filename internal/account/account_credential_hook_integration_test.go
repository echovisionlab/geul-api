//go:build integration

package account

import (
	"context"
	"errors"
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type recordingCredentialHookEmailPublisher struct {
	jobs []*managev1.SendEmailEvent
}

func (publisher *recordingCredentialHookEmailPublisher) PublishSendEmail(
	_ context.Context,
	job *managev1.SendEmailEvent,
) error {
	publisher.jobs = append(publisher.jobs, job)
	return nil
}

func TestAccountCredentialHookCompletesCommittedOIDCRemovalIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	emailAddress := "credential-hook-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: emailAddress,
	})
	memberID := seedActiveMemberEmailPair(t, db, identityID, emailAddress)
	manager := newCredentialMutationIdentityManager(identityID, emailAddress)
	manager.identity.ExternalID = memberID
	previousCredentials := map[string]auth.Credential{
		"oidc": manager.identity.Credentials["oidc"],
		"code": manager.identity.Credentials["code"],
	}
	delete(manager.identity.Credentials, "oidc")
	finalCredentials := map[string]auth.Credential{
		"code": manager.identity.Credentials["code"],
	}
	publisher := &recordingCredentialHookEmailPublisher{}
	lifecycle := NewAccountCredentialHookLifecycle(
		db,
		manager,
		apitelemetry.NewDurableWriter(db),
		publisher,
		memberEmailProjectionIntegration{},
	)
	auditID := uuid.NewString()
	input := AccountCredentialHookInput{
		AuditID:                   auditID,
		FlowID:                    "settings-flow-1",
		IdentityID:                identityID,
		Kind:                      AccountCredentialOIDC,
		PreviousCredentials:       previousCredentials,
		Credentials:               finalCredentials,
		PreviousSnapshotPresent:   true,
		CredentialSnapshotPresent: true,
	}

	require.NoError(t, lifecycle.Complete(t.Context(), input))
	require.NoError(t, lifecycle.Complete(t.Context(), input), "same Kratos trigger replay must converge")

	var audit struct {
		Count               int64
		Action              string
		TargetID            string
		ActorMemberID       string
		ChangedFields       pq.StringArray `gorm:"type:text[]"`
		CollectionOperation string
		Provider            string
		ProviderSubject     string
	}
	require.NoError(t, db.Raw(`
		SELECT COUNT(*) OVER () AS count, action, target_id,
		       actor_member_id::text AS actor_member_id,
		       ARRAY(SELECT jsonb_array_elements_text(attributes->'changed_fields')) AS changed_fields,
		       attributes->>'collection_operation' AS collection_operation,
		       attributes->>'provider' AS provider,
		       attributes->>'provider_subject' AS provider_subject
		FROM public.domain_audit
		WHERE audit_id = ?::uuid
	`, auditID).Scan(&audit).Error)
	require.Equal(t, int64(1), audit.Count)
	require.Equal(t, string(sharedtelemetry.AuditAccountUpdated), audit.Action)
	require.Equal(t, memberID, audit.TargetID)
	require.Equal(t, memberID, audit.ActorMemberID)
	require.Equal(t, pq.StringArray{"social_logins"}, audit.ChangedFields)
	require.Equal(t, string(sharedtelemetry.AuditCollectionOperationRemoved), audit.CollectionOperation)
	require.Equal(t, "google", audit.Provider)
	require.Equal(t, "subject", audit.ProviderSubject)
	require.Equal(t, []string{identityID, identityID}, manager.deletedSessions)
	require.Len(t, publisher.jobs, 2, "email.send is at-least-once across hook retries")
	require.Equal(t, publisher.jobs[0].GetMessageId(), publisher.jobs[1].GetMessageId())
	require.Contains(t, publisher.jobs[0].GetMessageId(), auditID)
}

func TestAccountCredentialHookCompletesCommittedPasskeyAdditionIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	emailAddress := "passkey-hook-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: emailAddress,
	})
	memberID := seedActiveMemberEmailPair(t, db, identityID, emailAddress)
	manager := newCredentialMutationIdentityManager(identityID, emailAddress)
	manager.identity.ExternalID = memberID
	delete(manager.identity.Credentials, "oidc")
	previousCredentials := map[string]auth.Credential{
		"code": manager.identity.Credentials["code"],
	}
	passkeyCredential := auth.Credential{
		Type: "passkey",
		Config: structured.Fields{"credentials": structured.Values{
			structured.Fields{"id": "passkey-2"},
			structured.Fields{"id": "passkey-1"},
		}},
	}
	manager.identity.Credentials["passkey"] = passkeyCredential
	finalCredentials := map[string]auth.Credential{
		"code":    manager.identity.Credentials["code"],
		"passkey": passkeyCredential,
	}
	publisher := &recordingCredentialHookEmailPublisher{}
	lifecycle := NewAccountCredentialHookLifecycle(
		db,
		manager,
		apitelemetry.NewDurableWriter(db),
		publisher,
		memberEmailProjectionIntegration{},
	)
	auditID := uuid.NewString()
	require.NoError(t, lifecycle.Complete(t.Context(), AccountCredentialHookInput{
		AuditID:                   auditID,
		FlowID:                    "settings-flow-1",
		IdentityID:                identityID,
		Kind:                      AccountCredentialPasskey,
		PreviousCredentials:       previousCredentials,
		Credentials:               finalCredentials,
		PreviousSnapshotPresent:   true,
		CredentialSnapshotPresent: true,
	}))

	var audit struct {
		ChangedFields       pq.StringArray `gorm:"type:text[]"`
		CollectionOperation string
		PasskeyIDs          pq.StringArray `gorm:"type:text[]"`
	}
	require.NoError(t, db.Raw(`
		SELECT ARRAY(SELECT jsonb_array_elements_text(attributes->'changed_fields')) AS changed_fields,
		       attributes->>'collection_operation' AS collection_operation,
		       ARRAY(SELECT jsonb_array_elements_text(attributes->'passkey_ids')) AS passkey_ids
		FROM public.domain_audit
		WHERE audit_id = ?::uuid
	`, auditID).Scan(&audit).Error)
	require.Equal(t, pq.StringArray{"passkeys"}, audit.ChangedFields)
	require.Equal(t, string(sharedtelemetry.AuditCollectionOperationAdded), audit.CollectionOperation)
	require.Equal(t, pq.StringArray{"passkey-1", "passkey-2"}, audit.PasskeyIDs)
	require.Empty(t, manager.deletedSessions)
	require.Len(t, publisher.jobs, 1)
	require.Equal(t, "passkey_added", publisher.jobs[0].GetTemplateType())
}

func TestAccountCredentialPreHookRejectsUnrecoverableAndUnbackedPrimaryIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	providerEmail := "provider-hook-" + identityID + "@example.test"
	recoveryEmail := "recovery-hook-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: providerEmail,
	})
	memberID := seedActiveMemberEmailPair(t, db, identityID, providerEmail)
	manager := &credentialMutationIdentityManager{adminAuthIdentityManager: &adminAuthIdentityManager{
		credentialScoped: true,
		identity: &auth.Identity{
			ID: identityID, ExternalID: memberID,
			Traits: structured.Fields{"email": providerEmail},
			VerifiableAddresses: []auth.VerifiableAddress{{
				Value: providerEmail, Via: "email", Verified: true,
			}},
			Credentials: map[string]auth.Credential{
				"oidc": {
					Type:        "oidc",
					Identifiers: []string{"google:subject"},
					Config: structured.Fields{"providers": structured.Values{structured.Fields{
						"provider": "google", "subject": "subject",
						"email": providerEmail, "email_verified": true,
					}}},
				},
				"code": {Type: "code", Identifiers: []string{recoveryEmail}},
			},
		},
	}}
	lifecycle := NewAccountCredentialHookLifecycle(
		db,
		manager,
		apitelemetry.NewDurableWriter(db),
		&recordingCredentialHookEmailPublisher{},
		memberEmailProjectionIntegration{},
	)
	passkeyOnly := map[string]auth.Credential{"passkey": {
		Type: "passkey",
		Config: structured.Fields{"credentials": structured.Values{
			structured.Fields{"id": "passkey-1"},
		}},
	}}

	require.ErrorIs(t, lifecycle.Validate(t.Context(), AccountCredentialHookInput{
		FlowID:                    "settings-flow-1",
		IdentityID:                identityID,
		Kind:                      AccountCredentialPasskey,
		PreviousCredentials:       passkeyOnly,
		Credentials:               passkeyOnly,
		PreviousSnapshotPresent:   true,
		CredentialSnapshotPresent: true,
	}), ErrAccountCredentialUnrecoverable)

	err := lifecycle.Validate(t.Context(), AccountCredentialHookInput{
		FlowID:              "settings-flow-2",
		IdentityID:          identityID,
		Kind:                AccountCredentialOIDC,
		PreviousCredentials: manager.identity.Credentials,
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{recoveryEmail}},
		},
		PreviousSnapshotPresent:   true,
		CredentialSnapshotPresent: true,
	})
	require.True(t, errors.Is(err, ErrMemberPrimaryEmailUnavailable), err)
	require.True(t, auth.NewCredentialInventory(manager.identity.Credentials).HasOIDCProvider("google", "subject"))
}
