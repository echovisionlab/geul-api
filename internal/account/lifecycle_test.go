package account_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/structured"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func lifecycleURLToken(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	token := parsed.Query().Get("token")
	require.NotEmpty(t, token)
	return token
}

func (f fixture) deletionRequest(t *testing.T, memberID string) account.DeletionRequest {
	t.Helper()
	for _, request := range f.store.DeletionRequests() {
		if request.MemberID == memberID {
			return request
		}
	}
	t.Fatalf("no deletion request of %s", memberID)
	return account.DeletionRequest{}
}

func requireAccountDeletionAuditTransitions(t *testing.T, f fixture, memberID string, want [][2]sharedtelemetry.AuditState) {
	t.Helper()
	var records []sharedtelemetry.AuditRecord
	for _, record := range f.audit.Records() {
		if record.TargetType == "account" && record.TargetID == memberID {
			records = append(records, record)
		}
	}
	require.Len(t, records, len(want))
	for index, transition := range want {
		require.Equal(t, sharedtelemetry.AuditAccountUpdated, records[index].Action)
		require.Equal(t, sharedtelemetry.ActorKindMember, records[index].Kind)
		require.Equal(t, memberID, records[index].MemberID)
		require.Equal(t, []string{"deletion_state"}, records[index].ChangedFields)
		require.Equal(t, transition[0], records[index].PreviousState)
		require.Equal(t, transition[1], records[index].NewState)
	}
}

func TestAccountLifecycleDeletionCanCancelAndRecoverCurrentPair(t *testing.T) {
	t.Parallel()
	t.Run("cancel", func(t *testing.T) {
		t.Parallel()
		checker := &authztest.Checker{}
		f := newFixture(t, checker)
		memberID, identityID := f.seedPair("")
		grantRole(f, checker, identityID, policyv1.Role.User())
		email := identityID + "@example.test"
		ctx := principalContext(t, memberID, identityID)

		_, err := f.service.RequestAccountDeletion(ctx)
		require.NoError(t, err)
		require.Len(t, f.emails.Sent(), 1)
		_, err = f.service.ConfirmAccountDeletion(ctx, account.ConfirmAccountDeletionCommand{Token: lifecycleURLToken(t, f.emails.Sent()[0].TemplateData["confirm_url"])})
		require.NoError(t, err)
		require.Equal(t, auth.KratosStateInactive, f.kratos.Identity(identityID).State)
		require.Contains(t, f.kratos.Writes(), "DeleteIdentitySessions:"+identityID)

		cancelToken := lifecycleURLToken(t, f.emails.Sent()[1].TemplateData["cancel_url"])
		require.NoError(t, f.service.CancelAccountDeletion(ctx, account.CancelAccountDeletionCommand{Token: cancelToken}))
		require.Equal(t, auth.KratosStateActive, f.kratos.Identity(identityID).State)
		request := f.deletionRequest(t, memberID)
		require.Equal(t, "cancelled", request.LifecycleState)
		require.Nil(t, request.ScheduledAt)
		require.Equal(t, email, *request.NotificationEmail)
		requireAccountDeletionAuditTransitions(t, f, memberID, [][2]sharedtelemetry.AuditState{
			{sharedtelemetry.AuditStateNone, sharedtelemetry.AuditStateConfirmationPending},
			{sharedtelemetry.AuditStateConfirmationPending, sharedtelemetry.AuditStateScheduled},
			{sharedtelemetry.AuditStateScheduled, sharedtelemetry.AuditStateCancelled},
		})
	})

	t.Run("recover", func(t *testing.T) {
		t.Parallel()
		checker := &authztest.Checker{}
		f := newFixture(t, checker)
		memberID, identityID := f.seedPair("")
		grantRole(f, checker, identityID, policyv1.Role.User())
		email := identityID + "@example.test"
		ctx := principalContext(t, memberID, identityID)
		_, err := f.service.RequestAccountDeletion(ctx)
		require.NoError(t, err)
		_, err = f.service.ConfirmAccountDeletion(ctx, account.ConfirmAccountDeletionCommand{Token: lifecycleURLToken(t, f.emails.Sent()[0].TemplateData["confirm_url"])})
		require.NoError(t, err)

		require.NoError(t, f.service.RequestAccountRecovery(ctx, account.RequestAccountRecoveryCommand{Email: email}))
		recoveryToken := lifecycleURLToken(t, f.emails.Sent()[2].TemplateData["confirm_url"])
		require.NoError(t, f.service.ConfirmAccountRecovery(ctx, account.ConfirmAccountRecoveryCommand{Token: recoveryToken}))
		require.Equal(t, auth.KratosStateActive, f.kratos.Identity(identityID).State)
		request := f.deletionRequest(t, memberID)
		require.Equal(t, "recovered", request.LifecycleState)
		require.Nil(t, request.ScheduledAt)
		requireAccountDeletionAuditTransitions(t, f, memberID, [][2]sharedtelemetry.AuditState{
			{sharedtelemetry.AuditStateNone, sharedtelemetry.AuditStateConfirmationPending},
			{sharedtelemetry.AuditStateConfirmationPending, sharedtelemetry.AuditStateScheduled},
			{sharedtelemetry.AuditStateRecoveryConfirmationPending, sharedtelemetry.AuditStateRecovered},
		})
	})
}

// The last admin deleting itself passes the administrator fence and stops
// at the last-admin fence.
func TestScheduleImmediateUserDeletionRejectsLastActiveAdmin(t *testing.T) {
	t.Parallel()
	checker := &authztest.Checker{}
	f := newFixture(t, checker)
	memberID, identityID := f.seedPair("")
	grantRole(f, checker, identityID, policyv1.Role.Admin())

	err := f.service.ScheduleImmediateDeletion(principalContext(t, memberID, identityID), memberID, identityID)

	require.ErrorIs(t, err, account.ErrLastActiveAdminDeletion)
	require.Empty(t, f.store.IdentityCommands())
	require.Empty(t, f.store.DeletionRequests())
	require.Equal(t, auth.KratosStateActive, f.kratos.Identity(identityID).State)
}

func TestRequestDeletionSynchronizesRemovedPrimaryBeforeSnapshot(t *testing.T) {
	t.Parallel()
	f := newFixture(t, authztest.AllowAll())
	memberID, identityID := f.seedPair("")
	fallbackEmail := "fallback-" + identityID + "@example.test"
	f.kratos.Update(identityID, func(identity *auth.Identity) {
		identity.Traits = structured.Fields{"email": fallbackEmail}
		identity.VerifiableAddresses = []auth.VerifiableAddress{{Via: "email", Value: fallbackEmail, Verified: true}}
		identity.Credentials = map[string]auth.Credential{"code": {Type: "code", Identifiers: []string{fallbackEmail}}}
	})

	_, err := f.service.RequestAccountDeletion(principalContext(t, memberID, identityID))
	require.NoError(t, err)
	require.Len(t, f.emails.Sent(), 1)
	require.Equal(t, fallbackEmail, f.emails.Sent()[0].Recipient)

	require.Equal(t, fallbackEmail, *f.deletionRequest(t, memberID).NotificationEmail)
	member, _ := f.store.Member(memberID)
	require.Equal(t, fallbackEmail, member.PrimaryEmail)
	require.Equal(t, []string{fallbackEmail}, member.AvailableEmails)
}
