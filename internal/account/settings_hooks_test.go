package account

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

type settingsHookIdentityReader struct {
	identity *auth.Identity
	err      error
}

func (reader settingsHookIdentityReader) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return reader.identity, reader.err
}

type settingsHookEmailChanges struct {
	stageInput         []string
	pendingWasVerified bool
	verifyInput        []string
	err                error
}

func (changes *settingsHookEmailChanges) StageOrCancel(
	_ context.Context,
	flowID, identityID, currentEmail, currentPendingEmail, candidatePending string,
	pendingVerified bool,
) error {
	changes.stageInput = []string{flowID, identityID, currentEmail, currentPendingEmail, candidatePending}
	changes.pendingWasVerified = pendingVerified
	return changes.err
}

func (changes *settingsHookEmailChanges) VerifyAndReconcile(
	_ context.Context,
	flowID, identityID, oldEmail, pendingEmail string,
) error {
	changes.verifyInput = []string{flowID, identityID, oldEmail, pendingEmail}
	return changes.err
}

func TestAccountSettingsHookStagesCommittedCanonicalEmail(t *testing.T) {
	changes := &settingsHookEmailChanges{}
	service := NewAccountSettingsHookService(settingsHookIdentityReader{identity: &auth.Identity{
		ID:                  "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Traits:              structured.Fields{"email": "old@example.test", "pending_email": "proven@example.test"},
		VerifiableAddresses: []auth.VerifiableAddress{{Value: "proven@example.test", Via: "email", Verified: true}},
	}}, changes)

	err := service.Stage(t.Context(), AccountSettingsHookInput{
		FlowID: "settings-flow", IdentityID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Email: " OLD@example.test ", PendingEmail: "new@example.test",
	})

	require.NoError(t, err)
	require.Equal(t, []string{
		"settings-flow", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"old@example.test", "proven@example.test", "new@example.test",
	}, changes.stageInput)
	require.True(t, changes.pendingWasVerified)
}

func TestAccountSettingsHookRejectsDirectCanonicalMutation(t *testing.T) {
	changes := &settingsHookEmailChanges{}
	service := NewAccountSettingsHookService(settingsHookIdentityReader{identity: &auth.Identity{
		Traits: structured.Fields{"email": "committed@example.test"},
	}}, changes)

	err := service.Stage(t.Context(), AccountSettingsHookInput{
		FlowID: "settings-flow", IdentityID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Email: "candidate@example.test",
	})

	require.ErrorIs(t, err, ErrCanonicalEmailChangeForbidden)
	require.Empty(t, changes.stageInput)
}

func TestAccountSettingsHookFailsClosedWhenIdentityCannotBeLoaded(t *testing.T) {
	service := NewAccountSettingsHookService(
		settingsHookIdentityReader{err: errors.New("Kratos unavailable")},
		&settingsHookEmailChanges{},
	)

	err := service.Stage(t.Context(), AccountSettingsHookInput{
		FlowID: "settings-flow", IdentityID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	})

	require.ErrorIs(t, err, ErrCanonicalEmailGuardFailed)
}

func TestAccountSettingsHookVerificationNoopsWithoutPendingEmail(t *testing.T) {
	changes := &settingsHookEmailChanges{}
	service := NewAccountSettingsHookService(settingsHookIdentityReader{}, changes)

	require.NoError(t, service.Verify(t.Context(), AccountSettingsHookInput{
		FlowID: "verification-flow", IdentityID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Email: "old@example.test",
	}))
	require.Empty(t, changes.verifyInput)

	require.NoError(t, service.Verify(t.Context(), AccountSettingsHookInput{
		FlowID: "verification-flow", IdentityID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Email: "old@example.test", PendingEmail: "new@example.test",
	}))
	require.Equal(t, []string{
		"verification-flow", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"old@example.test", "new@example.test",
	}, changes.verifyInput)
}
