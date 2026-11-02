package account_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/account/accounttest"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// flakyEmails fails a number of publishes, or accepts one and then reports
// a lost confirmation.
type flakyEmails struct {
	mu                  sync.Mutex
	failures            int
	acceptThenErrorOnce bool
	jobs                []account.Email
}

func (p *flakyEmails) PublishEmail(_ context.Context, job account.Email) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.acceptThenErrorOnce {
		p.acceptThenErrorOnce = false
		p.jobs = append(p.jobs, job)
		return errors.New("publisher confirm response lost after broker acceptance")
	}
	if p.failures > 0 {
		p.failures--
		return errors.New("queue unavailable")
	}
	p.jobs = append(p.jobs, job)
	return nil
}

func (p *flakyEmails) sent() []account.Email {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]account.Email(nil), p.jobs...)
}

type emailChangeFixture struct {
	store      *accounttest.Store
	units      *accounttest.Units
	kratos     *accounttest.Kratos
	identityID string
	memberID   string
}

// newEmailChangeFixture seeds an active pair whose canonical, verified and
// only email is previousEmail.
func newEmailChangeFixture(t *testing.T, previousEmail func(identityID string) string) (emailChangeFixture, string) {
	t.Helper()
	store, units := accounttest.New()
	fixture := emailChangeFixture{store: store, units: units, kratos: accounttest.NewKratos(store), identityID: uuid.NewString(), memberID: uuid.NewString()}
	email := previousEmail(fixture.identityID)
	fixture.seedIdentity(fixture.memberID, fixture.identityID, email)
	return fixture, email
}

func (f emailChangeFixture) seedIdentity(memberID, identityID, email string) {
	f.store.SeedPair(memberID, identityID, email)
	f.kratos.Put(&auth.Identity{
		ID: identityID, ExternalID: memberID, State: auth.KratosStateActive,
		Traits:              structured.Fields{"email": email},
		VerifiableAddresses: []auth.VerifiableAddress{{Value: email, Via: "email", Verified: true}},
		Credentials:         map[string]auth.Credential{"code": {Type: "code", Identifiers: []string{email}}},
	})
}

func (f emailChangeFixture) lifecycle(publisher account.EmailPublisher) *account.AccountEmailChangeLifecycle {
	return account.NewAccountEmailChangeLifecycle(f.units, f.kratos, publisher)
}

// setPending stores the pending address Kratos' settings flow persisted.
func (f emailChangeFixture) setPending(email string, verified bool) {
	f.kratos.Update(f.identityID, func(identity *auth.Identity) {
		identity.Traits["pending_email"] = email
		identity.VerifiableAddresses = append(identity.VerifiableAddresses, auth.VerifiableAddress{Value: email, Via: "email", Verified: verified})
	})
}

func (f emailChangeFixture) requests() []account.EmailChangeRequest {
	var requests []account.EmailChangeRequest
	for _, request := range f.store.EmailChanges() {
		if request.IdentityID == f.identityID {
			requests = append(requests, request)
		}
	}
	return requests
}

func (f emailChangeFixture) request(t *testing.T) account.EmailChangeRequest {
	t.Helper()
	requests := f.requests()
	require.Len(t, requests, 1)
	return requests[0]
}

func oldEmail(identityID string) string { return "old-" + identityID + "@example.test" }

func TestAccountEmailChangeRequestConvergesAndRetriesConfirmedNotification(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f, previousEmail := newEmailChangeFixture(t, oldEmail)
	requestedEmail := "new-" + f.identityID + "@example.test"
	publisher := &flakyEmails{failures: 1}
	lifecycle := f.lifecycle(publisher)

	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-flow", f.identityID, previousEmail, "", requestedEmail, false))
	request := f.request(t)
	require.Equal(t, previousEmail, request.PreviousEmailAddress)
	require.Equal(t, requestedEmail, request.RequestedEmailAddress)
	require.Equal(t, f.memberID, request.MemberID)

	f.setPending(requestedEmail, true)
	err := lifecycle.VerifyAndReconcile(ctx, "verification-flow", f.identityID, previousEmail, requestedEmail)
	require.ErrorContains(t, err, "queue unavailable")
	identity := f.kratos.Identity(f.identityID)
	require.Equal(t, requestedEmail, identity.CurrentEmail())
	require.Empty(t, identity.PendingEmail())
	require.Equal(t, request, f.request(t))

	require.NoError(t, lifecycle.ReconcileRequest(ctx, request.ID))
	require.Empty(t, f.requests())
	jobs := publisher.sent()
	require.Len(t, jobs, 1)
	require.Equal(t, previousEmail, jobs[0].Recipient)
	require.Equal(t, "account-email-change:"+request.ID, jobs[0].MessageID)

	member, found := f.store.Member(f.memberID)
	require.True(t, found)
	require.Equal(t, requestedEmail, member.PrimaryEmail)
	require.Contains(t, member.AvailableEmails, requestedEmail)
}

func TestAccountEmailChangeRequestReplacesOnlyUnverifiedActiveRequest(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f, previousEmail := newEmailChangeFixture(t, oldEmail)
	firstEmail := "first-" + f.identityID + "@example.test"
	secondEmail := "second-" + f.identityID + "@example.test"
	lifecycle := f.lifecycle(&flakyEmails{})

	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-1", f.identityID, previousEmail, "", firstEmail, false))
	f.setPending(firstEmail, false)
	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-2", f.identityID, previousEmail, firstEmail, secondEmail, false))
	require.Equal(t, secondEmail, f.request(t).RequestedEmailAddress)

	f.setPending(secondEmail, true)
	err := lifecycle.StageOrCancel(ctx, "settings-3", f.identityID, previousEmail, secondEmail, firstEmail, true)
	require.ErrorIs(t, err, account.ErrAccountEmailChangeInFlight)
	require.Equal(t, secondEmail, f.request(t).RequestedEmailAddress)
}

func TestAccountEmailChangeRequestDoesNotStartWhenKratosIsUnavailable(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f, previousEmail := newEmailChangeFixture(t, oldEmail)
	requestedEmail := "new-" + f.identityID + "@example.test"
	f.kratos.FailIdentity(f.identityID, errors.New("kratos unavailable"))
	lifecycle := f.lifecycle(&flakyEmails{})

	err := lifecycle.StageOrCancel(ctx, "settings-flow", f.identityID, previousEmail, "", requestedEmail, false)
	require.ErrorContains(t, err, "kratos unavailable")
	require.Empty(t, f.requests())
}

func TestAccountEmailChangeConflictClearsOnlyRequestedAddress(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f, previousEmail := newEmailChangeFixture(t, oldEmail)
	otherIdentityID := uuid.NewString()
	requestedEmail := "occupied-" + otherIdentityID + "@example.test"
	f.seedIdentity(uuid.NewString(), otherIdentityID, requestedEmail)
	lifecycle := f.lifecycle(&flakyEmails{})

	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-flow", f.identityID, previousEmail, "", requestedEmail, false))
	f.setPending(requestedEmail, true)
	err := lifecycle.VerifyAndReconcile(ctx, "verification-flow", f.identityID, previousEmail, requestedEmail)
	require.ErrorIs(t, err, account.ErrAccountEmailChangeConflict)
	identity := f.kratos.Identity(f.identityID)
	require.Equal(t, previousEmail, identity.CurrentEmail())
	require.Empty(t, identity.PendingEmail())
	require.True(t, identity.HasVerifiedEmailAddress(previousEmail))
	require.False(t, identity.HasVerifiedEmailAddress(requestedEmail))
	require.Empty(t, f.requests())
	require.Equal(t, requestedEmail, f.kratos.Identity(otherIdentityID).CurrentEmail())
}

func TestAccountEmailChangeExactPendingRequestSurvivesCodeExpiry(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f, previousEmail := newEmailChangeFixture(t, oldEmail)
	requestedEmail := "new-" + f.identityID + "@example.test"
	lifecycle := f.lifecycle(&flakyEmails{})

	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-flow", f.identityID, previousEmail, "", requestedEmail, false))
	f.setPending(requestedEmail, false)
	request := f.request(t)
	lifecycle.SetNow(func() time.Time { return request.CreatedAt.Add(account.AccountEmailChangeProofGrace + time.Second) })

	require.NoError(t, lifecycle.ReconcileRequest(ctx, request.ID))
	identity := f.kratos.Identity(f.identityID)
	require.Equal(t, previousEmail, identity.CurrentEmail())
	require.Equal(t, requestedEmail, identity.PendingEmail())
	require.Equal(t, request, f.request(t))
}

func TestAccountEmailChangeExactPendingRequestExpiresAndReleasesAddress(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f, previousEmail := newEmailChangeFixture(t, oldEmail)
	requestedEmail := "new-" + f.identityID + "@example.test"
	lifecycle := f.lifecycle(&flakyEmails{})

	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-flow", f.identityID, previousEmail, "", requestedEmail, false))
	f.setPending(requestedEmail, false)
	request := f.request(t)

	lifecycle.SetNow(func() time.Time {
		return request.CreatedAt.Add(account.AccountEmailChangePendingLifetime - time.Minute)
	})
	require.NoError(t, lifecycle.ReconcileRequest(ctx, request.ID))
	require.Equal(t, requestedEmail, f.kratos.Identity(f.identityID).PendingEmail(), "the owner keeps the request within its lifetime")

	lifecycle.SetNow(func() time.Time {
		return request.CreatedAt.Add(account.AccountEmailChangePendingLifetime + time.Second)
	})
	require.NoError(t, lifecycle.ReconcileRequest(ctx, request.ID))
	identity := f.kratos.Identity(f.identityID)
	require.Equal(t, previousEmail, identity.CurrentEmail())
	require.Empty(t, identity.PendingEmail(), "an expired request releases the pending address")
	require.False(t, identity.HasVerifiedEmailAddress(requestedEmail))
	for _, address := range identity.VerifiableAddresses {
		require.NotEqual(t, requestedEmail, address.Value, "the Kratos verifiable address no longer holds the address")
	}
	require.Empty(t, f.requests())
}

func TestAccountEmailChangeOrphanRequestIsRemovedAfterGrace(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f, previousEmail := newEmailChangeFixture(t, oldEmail)
	requestedEmail := "new-" + f.identityID + "@example.test"
	lifecycle := f.lifecycle(&flakyEmails{})

	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-flow", f.identityID, previousEmail, "", requestedEmail, false))
	request := f.request(t)
	lifecycle.SetNow(func() time.Time { return request.CreatedAt.Add(account.AccountEmailChangeProofGrace + time.Second) })

	require.NoError(t, lifecycle.ReconcileRequest(ctx, request.ID))
	require.Equal(t, previousEmail, f.kratos.Identity(f.identityID).CurrentEmail())
	require.Empty(t, f.requests())
}

func TestAccountEmailChangePrePersistFailureCanBeRetriedAndOrphanedReplacementExpires(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f, previousEmail := newEmailChangeFixture(t, oldEmail)
	firstEmail := "first-" + f.identityID + "@example.test"
	retryEmail := "retry-" + f.identityID + "@example.test"
	lifecycle := f.lifecycle(&flakyEmails{})

	// The first pre-persist hook committed its request, but Kratos rejected the
	// settings write, so no pending address exists when the user retries.
	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-failed", f.identityID, previousEmail, "", firstEmail, false))
	first := f.request(t)
	require.Equal(t, firstEmail, first.RequestedEmailAddress)

	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-retry", f.identityID, previousEmail, "", retryEmail, false))
	replacement := f.request(t)
	require.NotEqual(t, first.ID, replacement.ID)
	require.Equal(t, retryEmail, replacement.RequestedEmailAddress)

	// If the replacement also never reaches Kratos, only that orphan is
	// removed after the bounded pre-persist grace period.
	lifecycle.SetNow(func() time.Time { return replacement.CreatedAt.Add(account.AccountEmailChangeProofGrace + time.Second) })
	require.NoError(t, lifecycle.ReconcileRequest(ctx, replacement.ID))
	require.Empty(t, f.requests())
	applied := f.kratos.Identity(f.identityID)
	require.Equal(t, previousEmail, applied.CurrentEmail())
	require.Empty(t, applied.PendingEmail())
}

func TestAccountEmailChangeSettingsVerificationRequiresExactUnverifiedAddress(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f, previousEmail := newEmailChangeFixture(t, oldEmail)
	requestedEmail := "new-" + f.identityID + "@example.test"
	lifecycle := f.lifecycle(&flakyEmails{})
	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-flow", f.identityID, previousEmail, "", requestedEmail, false))

	authorized, err := lifecycle.AuthorizeSettingsGeneratedVerification(ctx, f.identityID, requestedEmail)
	require.NoError(t, err)
	require.False(t, authorized, "an active row alone must not authorize courier fallback")

	f.kratos.Update(f.identityID, func(identity *auth.Identity) { identity.Traits["pending_email"] = requestedEmail })
	authorized, err = lifecycle.AuthorizeSettingsGeneratedVerification(ctx, f.identityID, requestedEmail)
	require.NoError(t, err)
	require.False(t, authorized, "pending trait without an exact unverified address is insufficient")

	f.setPending(requestedEmail, false)
	authorized, err = lifecycle.AuthorizeSettingsGeneratedVerification(ctx, f.identityID, requestedEmail)
	require.NoError(t, err)
	require.True(t, authorized)

	authorized, err = lifecycle.AuthorizeSettingsGeneratedVerification(ctx, f.identityID, "different@example.test")
	require.NoError(t, err)
	require.False(t, authorized)
}

func TestAccountEmailChangeConfirmUncertaintyMayDuplicateStableCommand(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f, previousEmail := newEmailChangeFixture(t, oldEmail)
	requestedEmail := "new-" + f.identityID + "@example.test"
	publisher := &flakyEmails{acceptThenErrorOnce: true}
	lifecycle := f.lifecycle(publisher)

	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-flow", f.identityID, previousEmail, "", requestedEmail, false))
	request := f.request(t)
	f.setPending(requestedEmail, true)

	err := lifecycle.VerifyAndReconcile(ctx, "verification-flow", f.identityID, previousEmail, requestedEmail)
	require.ErrorContains(t, err, "confirm response lost")
	require.NoError(t, lifecycle.ReconcileRequest(ctx, request.ID))
	jobs := publisher.sent()
	require.Len(t, jobs, 2)
	require.Equal(t, jobs[0].MessageID, jobs[1].MessageID)
	require.Equal(t, "account-email-change:"+request.ID, jobs[0].MessageID)
	require.Empty(t, f.requests())
}

func TestAccountEmailChangeReconcileScansPastFailingFirstBatch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, units := accounttest.New()
	f := emailChangeFixture{store: store, units: units, kratos: accounttest.NewKratos(store)}
	baseTime := time.Now().UTC().Add(-account.AccountEmailChangeProofGrace - time.Minute)
	var last account.EmailChangeRequest
	for i := range account.AccountEmailChangeBatchSize + 1 {
		memberID, identityID := uuid.NewString(), uuid.NewString()
		previousEmail := oldEmail(identityID)
		f.seedIdentity(memberID, identityID, previousEmail)
		request := account.EmailChangeRequest{
			ID: uuid.NewString(), MemberID: memberID, IdentityID: identityID,
			PreviousEmailAddress: previousEmail, RequestedEmailAddress: "new-" + identityID + "@example.test",
			CreatedAt: baseTime.Add(time.Duration(i) * time.Millisecond),
		}
		store.SeedEmailChange(request)
		if i < account.AccountEmailChangeBatchSize {
			f.kratos.FailIdentity(identityID, errors.New("identity backend unavailable"))
		} else {
			last = request
		}
	}

	require.NoError(t, f.lifecycle(&flakyEmails{}).Reconcile(ctx))

	remaining := store.EmailChanges()
	require.NotContains(t, remaining, last.ID, "a failing first page must not starve later requests")
	require.Len(t, remaining, account.AccountEmailChangeBatchSize)
}

func TestAccountEmailChangeStagesInOneStepUnderTheIdentityFence(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f, previousEmail := newEmailChangeFixture(t, oldEmail)
	requestedEmail := "new-" + f.identityID + "@example.test"

	require.NoError(t, f.lifecycle(&flakyEmails{}).StageOrCancel(ctx, "settings-flow", f.identityID, previousEmail, "", requestedEmail, false))
	require.Equal(t, []string{f.identityID}, f.units.FencedIdentities())
	require.Equal(t, []string{"GetIdentity:" + f.identityID}, onlyIdentityCalls(f.kratos.Calls(), f.identityID))
	require.Empty(t, f.kratos.Writes())
}

func onlyIdentityCalls(calls []string, identityID string) []string {
	var result []string
	for _, call := range calls {
		if strings.HasSuffix(call, ":"+identityID) {
			result = append(result, call)
		}
	}
	return result
}
