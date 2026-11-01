package emaildelivery_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/emaildelivery/emaildeliverytest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func manualSuppression(email string) emaildelivery.Suppression {
	return emaildelivery.Suppression{
		Email: email, Reason: emaildelivery.EmailSuppressionReasonManual, Source: emaildelivery.EmailSuppressionSourceAdmin,
		SuppressedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
}

func TestReleaseEmailSuppressionAuditsTheExactTransitionOnce(t *testing.T) {
	f := newFixture(t)
	suppression := f.store.SeedSuppression(manualSuppression("suppressed-audit@example.test"))
	handler := emaildelivery.NewEmailSuppressionConnectHandler(f.delivery)
	request := connect.NewRequest(&managev1.ReleaseEmailSuppressionRequest{Email: " Suppressed-Audit@Example.test "})

	_, err := handler.ReleaseEmailSuppression(f.admin.ctx, request)
	require.NoError(t, err)
	_, err = handler.ReleaseEmailSuppression(f.admin.ctx, request)
	require.NoError(t, err)

	released := f.store.Suppressions(suppression.Email)
	require.Len(t, released, 1)
	require.NotNil(t, released[0].ReleasedAt)
	require.Equal(t, f.admin.memberID, *released[0].ReleasedBy)
	records := f.audit.Records()
	require.Len(t, records, 1, "releasing an address without an active suppression is unaudited")
	require.Equal(t, sharedtelemetry.AuditEmailSuppressionUpdated, records[0].Action)
	require.Equal(t, "email_suppression", records[0].TargetType)
	require.Equal(t, suppression.ID, records[0].TargetID)
	require.Equal(t, []string{"status"}, records[0].ChangedFields)
	require.Equal(t, sharedtelemetry.AuditStateActive, records[0].PreviousState)
	require.Equal(t, sharedtelemetry.AuditStateReleased, records[0].NewState)
	require.Equal(t, []mutation.Step{
		mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepCommit,
		mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepCommit,
	}, f.units.Steps())
}

func TestReleaseEmailSuppressionAuditFailureRollsBackTheRelease(t *testing.T) {
	f := newFixtureWith(t, fixtureOptions{audit: &audittest.Recorder{Err: errAuditDown}})
	suppression := f.store.SeedSuppression(manualSuppression("suppressed-rollback@example.test"))

	err := f.delivery.ReleaseEmailSuppression(f.admin.ctx, emaildelivery.ReleaseEmailSuppressionCommand{Email: suppression.Email})

	require.ErrorIs(t, err, errAuditDown)
	_, err = emaildelivery.NewEmailSuppressionConnectHandler(f.delivery).ReleaseEmailSuppression(f.admin.ctx,
		connect.NewRequest(&managev1.ReleaseEmailSuppressionRequest{Email: suppression.Email}))
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	stored := f.store.Suppressions(suppression.Email)
	require.Len(t, stored, 1)
	require.Nil(t, stored[0].ReleasedAt)
	require.Nil(t, stored[0].ReleasedBy)
	require.Equal(t, mutation.StepRollback, f.units.Steps()[len(f.units.Steps())-1])
}

func TestReleaseEmailSuppressionAuthorizesBeforeValidating(t *testing.T) {
	f := newFixture(t)
	suppression := f.store.SeedSuppression(manualSuppression("kept@example.test"))

	err := f.delivery.ReleaseEmailSuppression(f.outsider.ctx, emaildelivery.ReleaseEmailSuppressionCommand{Email: suppression.Email})
	requireAnswer(t, connect.CodePermissionDenied, "admin access required", err)
	err = f.delivery.ReleaseEmailSuppression(f.outsider.ctx, emaildelivery.ReleaseEmailSuppressionCommand{Email: "  "})
	requireAnswer(t, connect.CodePermissionDenied, "admin access required", err)
	err = f.delivery.ReleaseEmailSuppression(f.admin.ctx, emaildelivery.ReleaseEmailSuppressionCommand{Email: "  "})
	requireAnswer(t, connect.CodeInvalidArgument, "email is required", err)

	require.Nil(t, f.store.Suppressions(suppression.Email)[0].ReleasedAt)
	require.Empty(t, f.audit.Records())
}

func TestGetEmailSuppressionRequiresAnAdministrator(t *testing.T) {
	f := newFixture(t)
	suppression := f.store.SeedSuppression(manualSuppression("suppressed-lookup@example.test"))
	handler := emaildelivery.NewEmailSuppressionConnectHandler(f.delivery)
	request := connect.NewRequest(&managev1.GetEmailSuppressionRequest{Email: " Suppressed-Lookup@example.test"})

	response, err := handler.GetEmailSuppression(f.admin.ctx, request)
	require.NoError(t, err)
	require.Equal(t, suppression.ID, response.Msg.GetSuppression().GetId())
	require.Equal(t, emaildelivery.EmailSuppressionReasonManual, response.Msg.GetSuppression().GetReason())

	none, err := handler.GetEmailSuppression(f.admin.ctx, connect.NewRequest(&managev1.GetEmailSuppressionRequest{Email: "free@example.test"}))
	require.NoError(t, err)
	require.Nil(t, none.Msg.GetSuppression())
	_, err = handler.GetEmailSuppression(f.admin.ctx, connect.NewRequest(&managev1.GetEmailSuppressionRequest{}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	f.checker.For(f.admin.identityID).Deny(policyv1.Platform.IsAdmin())
	_, err = handler.GetEmailSuppression(f.admin.ctx, request)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, err = handler.GetEmailSuppression(f.outsider.ctx, request)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// suppress records request as the worker and the SES callback do, on the
// store their transaction binds.
func suppress(t *testing.T, store *emaildeliverytest.Store, request emaildelivery.SuppressionRequest) {
	t.Helper()
	require.NoError(t, emaildelivery.SuppressAddress(t.Context(), store, request))
}

func sesComplaint(email, providerMessageID string) emaildelivery.SuppressionRequest {
	return emaildelivery.SuppressionRequest{
		Email: email, Reason: emaildelivery.EmailSuppressionReasonSESComplaint,
		Source: emaildelivery.EmailSuppressionSourceSESCallback, ReferenceID: providerMessageID, ErrorType: "complaint",
	}
}

func TestSESSuppressionsDeduplicateReplaysAndNeverRegress(t *testing.T) {
	store := emaildeliverytest.NewStore()
	const address = "ses-callback@example.test"

	suppress(t, store, sesComplaint(" SES-Callback@Example.test ", " provider-message-1 "))
	first := store.Suppressions(address)
	require.Len(t, first, 1)
	require.Equal(t, address, first[0].Email, "the address is stored normalized")
	require.Equal(t, "provider-message-1", *first[0].ReferenceID)
	require.Equal(t, "complaint", *first[0].LastError)

	suppress(t, store, sesComplaint(address, "provider-message-1"))
	replayed := store.Suppressions(address)
	require.Len(t, replayed, 1)
	require.Equal(t, first[0].SuppressedAt, replayed[0].SuppressedAt, "a callback replay changes nothing")

	lateBounce := sesComplaint(address, "provider-message-1")
	lateBounce.Reason, lateBounce.ErrorType = emaildelivery.EmailSuppressionReasonSESBounce, "late permanent bounce"
	suppress(t, store, lateBounce)
	afterBounce := store.Suppressions(address)
	require.Len(t, afterBounce, 1)
	require.Equal(t, emaildelivery.EmailSuppressionReasonSESComplaint, afterBounce[0].Reason, "a complaint never regresses to a bounce")
	require.Equal(t, first[0].SuppressedAt, afterBounce[0].SuppressedAt)
}

func TestReleasedSESSuppressionIsNotRestoredByAReplayedCallback(t *testing.T) {
	f := newFixture(t)
	const address = "released-replay@example.test"
	suppress(t, f.store, sesComplaint(address, "provider-message-1"))
	require.NoError(t, f.delivery.ReleaseEmailSuppression(f.admin.ctx, emaildelivery.ReleaseEmailSuppressionCommand{Email: address}))

	suppress(t, f.store, sesComplaint(address, "provider-message-1"))

	suppression, err := emaildelivery.FindActiveSuppression(t.Context(), f.store, address)
	require.NoError(t, err)
	require.Nil(t, suppression, "the replayed complaint does not suppress the released address again")
	suppress(t, f.store, sesComplaint(address, "provider-message-2"))
	suppression, err = emaildelivery.FindActiveSuppression(t.Context(), f.store, address)
	require.NoError(t, err)
	require.NotNil(t, suppression, "a new complaint suppresses it again")
	require.Len(t, f.store.Suppressions(address), 2)
}

func TestSESSuppressionsUpgradeAWeakerReasonAndRespectAnAdministrator(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing emaildelivery.Suppression
		incoming string
		replaced bool
	}{
		{"admin suppression is authoritative", emaildelivery.Suppression{Reason: emaildelivery.EmailSuppressionReasonInvalidRecipient, Source: emaildelivery.EmailSuppressionSourceAdmin}, emaildelivery.EmailSuppressionReasonSESComplaint, false},
		{"manual suppression is authoritative", emaildelivery.Suppression{Reason: emaildelivery.EmailSuppressionReasonManual, Source: emaildelivery.EmailSuppressionSourceEmailWorker}, emaildelivery.EmailSuppressionReasonSESComplaint, false},
		{"complaint cannot regress to bounce", emaildelivery.Suppression{Reason: emaildelivery.EmailSuppressionReasonSESComplaint, Source: emaildelivery.EmailSuppressionSourceSESCallback}, emaildelivery.EmailSuppressionReasonSESBounce, false},
		{"duplicate complaint is a no-op", emaildelivery.Suppression{Reason: emaildelivery.EmailSuppressionReasonSESComplaint, Source: emaildelivery.EmailSuppressionSourceSESCallback}, emaildelivery.EmailSuppressionReasonSESComplaint, false},
		{"bounce upgrades to complaint", emaildelivery.Suppression{Reason: emaildelivery.EmailSuppressionReasonSESBounce, Source: emaildelivery.EmailSuppressionSourceSESCallback}, emaildelivery.EmailSuppressionReasonSESComplaint, true},
		{"invalid recipient upgrades to bounce", emaildelivery.Suppression{Reason: emaildelivery.EmailSuppressionReasonInvalidRecipient, Source: emaildelivery.EmailSuppressionSourceEmailWorker}, emaildelivery.EmailSuppressionReasonSESBounce, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := emaildeliverytest.NewStore()
			tc.existing.Email = "upgrade@example.test"
			existing := store.SeedSuppression(tc.existing)
			request := sesComplaint(existing.Email, "provider-message-new")
			request.Reason = tc.incoming

			suppress(t, store, request)

			stored := store.Suppressions(existing.Email)
			require.Len(t, stored, 1)
			require.Equal(t, existing.ID, stored[0].ID)
			if !tc.replaced {
				require.Equal(t, existing, stored[0])
				return
			}
			require.Equal(t, tc.incoming, stored[0].Reason)
			require.Equal(t, emaildelivery.EmailSuppressionSourceSESCallback, stored[0].Source)
			require.Equal(t, "provider-message-new", *stored[0].ReferenceID)
			require.True(t, stored[0].SuppressedAt.After(existing.SuppressedAt) || stored[0].SuppressedAt.Equal(existing.SuppressedAt))
		})
	}
}

func TestWorkerSuppressionDefaultsToAnInvalidRecipient(t *testing.T) {
	store := emaildeliverytest.NewStore()

	suppress(t, store, emaildelivery.SuppressionRequest{Email: "Invalid@Example.test", ReferenceID: "  ", ErrorType: " 550 user unknown "})
	suppress(t, store, emaildelivery.SuppressionRequest{Email: "invalid@example.test", ErrorType: "550 again"})
	suppress(t, store, emaildelivery.SuppressionRequest{Email: "   "})

	stored := store.Suppressions("invalid@example.test")
	require.Len(t, stored, 1)
	require.Equal(t, emaildelivery.EmailSuppressionReasonInvalidRecipient, stored[0].Reason)
	require.Equal(t, emaildelivery.EmailSuppressionSourceEmailWorker, stored[0].Source)
	require.Nil(t, stored[0].ReferenceID, "a blank reference is none")
	require.Equal(t, "550 user unknown", *stored[0].LastError, "the same suppression again is a no-op")
}

func TestSuppressAddressStopsAtAFailedLookup(t *testing.T) {
	store := emaildeliverytest.NewStore()
	store.Fail = map[string]error{"SuppressionRecorded": errAuditDown}
	require.ErrorIs(t, emaildelivery.SuppressAddress(t.Context(), store, sesComplaint("failed@example.test", "message")), errAuditDown)
	store.Fail = map[string]error{"LockActiveSuppression": errAuditDown}
	require.ErrorIs(t, emaildelivery.SuppressAddress(t.Context(), store, sesComplaint("failed@example.test", "message")), errAuditDown)
	require.Empty(t, store.Suppressions("failed@example.test"))
}

func TestActiveSuppressionLookupsNormalizeAddresses(t *testing.T) {
	store := emaildeliverytest.NewStore()
	store.SeedSuppression(manualSuppression("found@example.test"))

	found, err := emaildelivery.FindActiveSuppression(t.Context(), store, " FOUND@example.test ")
	require.NoError(t, err)
	require.Equal(t, emaildelivery.EmailSuppressionReasonManual, found.Reason)
	none, err := emaildelivery.FindActiveSuppression(t.Context(), store, "  ")
	require.NoError(t, err)
	require.Nil(t, none)

	byAddress, err := emaildelivery.LookupActiveSuppressions(t.Context(), store, []string{"", " Found@Example.test", "found@example.test", "other@example.test"})
	require.NoError(t, err)
	require.Len(t, byAddress, 1)
	require.Equal(t, emaildelivery.EmailSuppressionReasonManual, byAddress["found@example.test"].Reason)
	empty, err := emaildelivery.LookupActiveSuppressions(t.Context(), store, []string{" "})
	require.NoError(t, err)
	require.Empty(t, empty)
}
