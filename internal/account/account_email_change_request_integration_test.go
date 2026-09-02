//go:build integration

package account

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type accountEmailChangeIdentityFixture struct {
	auth.IdentityManager

	mu               sync.Mutex
	identity         *auth.Identity
	otherIdentities  []*auth.Identity
	getErr           error
	updateAccountErr error
}

func (f *accountEmailChangeIdentityFixture) GetIdentity(context.Context, string) (*auth.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.identity == nil {
		return nil, nil
	}
	copy := *f.identity
	copy.Traits = make(map[string]interface{}, len(f.identity.Traits))
	for key, value := range f.identity.Traits {
		copy.Traits[key] = value
	}
	copy.Credentials = make(map[string]auth.Credential, len(f.identity.Credentials))
	for key, value := range f.identity.Credentials {
		copy.Credentials[key] = value
	}
	copy.VerifiableAddresses = append([]auth.VerifiableAddress(nil), f.identity.VerifiableAddresses...)
	return &copy, nil
}

func (f *accountEmailChangeIdentityFixture) GetIdentityWithIncludeCredential(
	ctx context.Context,
	identityID string,
	_ string,
) (*auth.Identity, error) {
	return f.GetIdentity(ctx, identityID)
}

func (f *accountEmailChangeIdentityFixture) FindIdentityByCredentialIdentifier(
	_ context.Context,
	identifier string,
) (*auth.Identity, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	if f.identity == nil {
		return nil, false, nil
	}
	if credential, ok := f.identity.Credentials["code"]; ok {
		for _, candidate := range credential.Identifiers {
			if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(identifier)) {
				return f.identity, true, nil
			}
		}
	}
	for _, other := range f.otherIdentities {
		if other == nil {
			continue
		}
		credential, ok := other.Credentials["code"]
		if !ok {
			continue
		}
		for _, candidate := range credential.Identifiers {
			if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(identifier)) {
				return other, true, nil
			}
		}
	}
	return nil, false, nil
}

func (f *accountEmailChangeIdentityFixture) UpdateIdentityTraits(
	_ context.Context,
	_ string,
	traits map[string]interface{},
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, value := range traits {
		if value == nil {
			delete(f.identity.Traits, key)
			continue
		}
		f.identity.Traits[key] = value
	}
	return nil
}

func (f *accountEmailChangeIdentityFixture) UpdateIdentityAccountEmailState(
	_ context.Context,
	_ string,
	currentEmail *string,
	traits map[string]interface{},
	addresses []auth.VerifiableAddress,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateAccountErr != nil {
		return f.updateAccountErr
	}
	if currentEmail != nil {
		email := strings.ToLower(strings.TrimSpace(*currentEmail))
		f.identity.Traits["email"] = email
		f.identity.Credentials["code"] = auth.Credential{Type: "code", Identifiers: []string{email}}
	}
	for key, value := range traits {
		if value == nil {
			delete(f.identity.Traits, key)
			continue
		}
		f.identity.Traits[key] = value
	}
	if addresses != nil {
		f.identity.VerifiableAddresses = addresses
	}
	return nil
}

type accountEmailChangePublisherFixture struct {
	mu                    sync.Mutex
	failures              int
	acceptThenErrorOnce   bool
	acceptedThenErrorDone bool
	jobs                  []*managev1.SendEmailEvent
}

type accountEmailChangeMultiIdentityFixture struct {
	auth.IdentityManager

	identities map[string]*auth.Identity
	errors     map[string]error
}

func (f *accountEmailChangeMultiIdentityFixture) GetIdentity(
	_ context.Context,
	identityID string,
) (*auth.Identity, error) {
	if err := f.errors[identityID]; err != nil {
		return nil, err
	}
	return f.identities[identityID], nil
}

func (f *accountEmailChangeMultiIdentityFixture) GetIdentityWithIncludeCredential(
	ctx context.Context,
	identityID string,
	_ string,
) (*auth.Identity, error) {
	return f.GetIdentity(ctx, identityID)
}

func (f *accountEmailChangeMultiIdentityFixture) FindIdentityByCredentialIdentifier(
	_ context.Context,
	identifier string,
) (*auth.Identity, bool, error) {
	for _, identity := range f.identities {
		if identity == nil {
			continue
		}
		credential, ok := identity.Credentials["code"]
		if !ok {
			continue
		}
		for _, candidate := range credential.Identifiers {
			if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(identifier)) {
				return identity, true, nil
			}
		}
	}
	return nil, false, nil
}

func (f *accountEmailChangeMultiIdentityFixture) UpdateIdentityAccountEmailState(
	context.Context,
	string,
	*string,
	map[string]interface{},
	[]auth.VerifiableAddress,
) error {
	return nil
}

func (p *accountEmailChangePublisherFixture) PublishSendEmail(
	_ context.Context,
	job *managev1.SendEmailEvent,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.acceptThenErrorOnce && !p.acceptedThenErrorDone {
		p.acceptedThenErrorDone = true
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

func TestAccountEmailChangeRequestMatchesCurrentMigrationSchemaIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)

	var columns []string
	require.NoError(t, db.Raw(`
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'account_email_change_request'
		ORDER BY ordinal_position
	`).Scan(&columns).Error)
	require.Equal(t, []string{
		"id",
		"identity_id",
		"previous_email_address",
		"requested_email_address",
		"created_at",
		"member_id",
	}, columns)
}

func TestAccountEmailChangeRequestConvergesAndRetriesConfirmedNotificationIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	ctx := t.Context()
	identityID := integrationTestUUID()
	cleanupAccountEmailChangeFixtures(t, db, identityID)
	previousEmail := "old-" + identityID + "@example.test"
	requestedEmail := "new-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: previousEmail})
	identity := newAccountEmailChangeIdentityFixture(t, db, identityID, previousEmail)
	publisher := &accountEmailChangePublisherFixture{failures: 1}
	lifecycle := NewAccountEmailChangeLifecycle(db, identity, publisher, memberEmailProjectionIntegration{})

	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-flow", identityID, previousEmail, "", requestedEmail, false))
	var request model.AccountEmailChangeRequest
	require.NoError(t, db.First(&request, "identity_id = ?::uuid", identityID).Error)
	require.Equal(t, previousEmail, request.PreviousEmailAddress)
	require.Equal(t, requestedEmail, request.RequestedEmailAddress)

	setPendingAccountEmail(identity, requestedEmail, true)
	err := lifecycle.VerifyAndReconcile(ctx, "verification-flow", identityID, previousEmail, requestedEmail)
	require.ErrorContains(t, err, "queue unavailable")
	require.Equal(t, requestedEmail, identity.identity.CurrentEmail())
	require.Empty(t, identity.identity.PendingEmail())
	require.NoError(t, db.First(&request, "id = ?", request.ID).Error)

	require.NoError(t, lifecycle.ReconcileRequest(ctx, request.ID))
	require.ErrorIs(t, db.First(&model.AccountEmailChangeRequest{}, "id = ?", request.ID).Error, gorm.ErrRecordNotFound)
	require.Len(t, publisher.jobs, 1)
	require.Equal(t, previousEmail, publisher.jobs[0].GetRecipient())
	require.Equal(t, "account-email-change:"+request.ID, publisher.jobs[0].GetMessageId())

	var member model.Member
	require.NoError(t, db.Where("account_identity_id = ?::uuid", identityID).Take(&member).Error)
	require.Equal(t, requestedEmail, *member.PrimaryEmail)
	require.Contains(t, []string(member.AvailableEmails), requestedEmail)
}

func TestAccountEmailChangeRequestReplacesOnlyUnverifiedActiveRequestIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	ctx := t.Context()
	identityID := integrationTestUUID()
	cleanupAccountEmailChangeFixtures(t, db, identityID)
	previousEmail := "old-" + identityID + "@example.test"
	firstEmail := "first-" + identityID + "@example.test"
	secondEmail := "second-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: previousEmail})
	identity := newAccountEmailChangeIdentityFixture(t, db, identityID, previousEmail)
	lifecycle := NewAccountEmailChangeLifecycle(db, identity, &accountEmailChangePublisherFixture{}, memberEmailProjectionIntegration{})

	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-1", identityID, previousEmail, "", firstEmail, false))
	setPendingAccountEmail(identity, firstEmail, false)
	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-2", identityID, previousEmail, firstEmail, secondEmail, false))

	var requests []model.AccountEmailChangeRequest
	require.NoError(t, db.Where("identity_id = ?::uuid", identityID).Find(&requests).Error)
	require.Len(t, requests, 1)
	require.Equal(t, secondEmail, requests[0].RequestedEmailAddress)

	setPendingAccountEmail(identity, secondEmail, true)
	err := lifecycle.StageOrCancel(ctx, "settings-3", identityID, previousEmail, secondEmail, firstEmail, true)
	require.ErrorIs(t, err, ErrAccountEmailChangeInFlight)
	require.NoError(t, db.Where("identity_id = ?::uuid", identityID).Find(&requests).Error)
	require.Len(t, requests, 1)
	require.Equal(t, secondEmail, requests[0].RequestedEmailAddress)
}

func TestAccountEmailChangeRequestDoesNotStartWhenKratosIsUnavailableIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	ctx := t.Context()
	identityID := integrationTestUUID()
	cleanupAccountEmailChangeFixtures(t, db, identityID)
	previousEmail := "old-" + identityID + "@example.test"
	requestedEmail := "new-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: previousEmail})
	identity := newAccountEmailChangeIdentityFixture(t, db, identityID, previousEmail)
	identity.getErr = errors.New("kratos unavailable")
	lifecycle := NewAccountEmailChangeLifecycle(db, identity, &accountEmailChangePublisherFixture{}, memberEmailProjectionIntegration{})

	err := lifecycle.StageOrCancel(ctx, "settings-flow", identityID, previousEmail, "", requestedEmail, false)
	require.ErrorContains(t, err, "kratos unavailable")
	var count int64
	require.NoError(t, db.Model(&model.AccountEmailChangeRequest{}).Where("identity_id = ?::uuid", identityID).Count(&count).Error)
	require.Zero(t, count)
}

func TestAccountEmailChangeConflictClearsOnlyRequestedAddressIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	ctx := t.Context()
	identityID := integrationTestUUID()
	otherIdentityID := integrationTestUUID()
	cleanupAccountEmailChangeFixtures(t, db, identityID, otherIdentityID)
	previousEmail := "old-" + identityID + "@example.test"
	requestedEmail := "occupied-" + otherIdentityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: previousEmail})
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: otherIdentityID, Email: requestedEmail})
	otherMemberID := seedActiveMemberEmailPair(t, db, otherIdentityID, requestedEmail)
	identity := newAccountEmailChangeIdentityFixture(t, db, identityID, previousEmail)
	identity.otherIdentities = []*auth.Identity{{
		ID:         otherIdentityID,
		ExternalID: otherMemberID,
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{requestedEmail}},
		},
	}}
	lifecycle := NewAccountEmailChangeLifecycle(db, identity, &accountEmailChangePublisherFixture{}, memberEmailProjectionIntegration{})

	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-flow", identityID, previousEmail, "", requestedEmail, false))
	setPendingAccountEmail(identity, requestedEmail, true)
	err := lifecycle.VerifyAndReconcile(ctx, "verification-flow", identityID, previousEmail, requestedEmail)
	require.ErrorIs(t, err, ErrAccountEmailChangeConflict)
	require.Equal(t, previousEmail, identity.identity.CurrentEmail())
	require.Empty(t, identity.identity.PendingEmail())
	require.True(t, identity.identity.HasVerifiedEmailAddress(previousEmail))
	require.False(t, identity.identity.HasVerifiedEmailAddress(requestedEmail))

	var count int64
	require.NoError(t, db.Model(&model.AccountEmailChangeRequest{}).Where("identity_id = ?::uuid", identityID).Count(&count).Error)
	require.Zero(t, count)
}

func TestAccountEmailChangeExactPendingRequestSurvivesCodeExpiryIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	ctx := t.Context()
	identityID := integrationTestUUID()
	cleanupAccountEmailChangeFixtures(t, db, identityID)
	previousEmail := "old-" + identityID + "@example.test"
	requestedEmail := "new-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: previousEmail})
	identity := newAccountEmailChangeIdentityFixture(t, db, identityID, previousEmail)
	lifecycle := NewAccountEmailChangeLifecycle(db, identity, &accountEmailChangePublisherFixture{}, memberEmailProjectionIntegration{})

	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-flow", identityID, previousEmail, "", requestedEmail, false))
	setPendingAccountEmail(identity, requestedEmail, false)
	var request model.AccountEmailChangeRequest
	require.NoError(t, db.First(&request, "identity_id = ?::uuid", identityID).Error)
	lifecycle.now = func() time.Time { return request.CreatedAt.Add(accountEmailChangeProofGrace + time.Second) }

	require.NoError(t, lifecycle.ReconcileRequest(ctx, request.ID))
	require.Equal(t, previousEmail, identity.identity.CurrentEmail())
	require.Equal(t, requestedEmail, identity.identity.PendingEmail())
	require.NoError(t, db.First(&model.AccountEmailChangeRequest{}, "id = ?", request.ID).Error)
}

func TestAccountEmailChangeOrphanRequestIsRemovedAfterGraceIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	ctx := t.Context()
	identityID := integrationTestUUID()
	cleanupAccountEmailChangeFixtures(t, db, identityID)
	previousEmail := "old-" + identityID + "@example.test"
	requestedEmail := "new-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: previousEmail})
	identity := newAccountEmailChangeIdentityFixture(t, db, identityID, previousEmail)
	lifecycle := NewAccountEmailChangeLifecycle(db, identity, &accountEmailChangePublisherFixture{}, memberEmailProjectionIntegration{})

	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-flow", identityID, previousEmail, "", requestedEmail, false))
	var request model.AccountEmailChangeRequest
	require.NoError(t, db.First(&request, "identity_id = ?::uuid", identityID).Error)
	lifecycle.now = func() time.Time { return request.CreatedAt.Add(accountEmailChangeProofGrace + time.Second) }

	require.NoError(t, lifecycle.ReconcileRequest(ctx, request.ID))
	require.Equal(t, previousEmail, identity.identity.CurrentEmail())
	require.ErrorIs(t, db.First(&model.AccountEmailChangeRequest{}, "id = ?", request.ID).Error, gorm.ErrRecordNotFound)
}

func TestAccountEmailChangePrePersistFailureCanBeRetriedAndOrphanedReplacementExpiresIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	ctx := t.Context()
	identityID := integrationTestUUID()
	cleanupAccountEmailChangeFixtures(t, db, identityID)
	previousEmail := "old-" + identityID + "@example.test"
	firstEmail := "first-" + identityID + "@example.test"
	retryEmail := "retry-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: previousEmail})
	identity := newAccountEmailChangeIdentityFixture(t, db, identityID, previousEmail)
	lifecycle := NewAccountEmailChangeLifecycle(db, identity, &accountEmailChangePublisherFixture{}, memberEmailProjectionIntegration{})

	// The first pre-persist hook committed its request, but Kratos rejected the
	// settings write, so no pending address exists when the user retries.
	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-failed", identityID, previousEmail, "", firstEmail, false))
	var first model.AccountEmailChangeRequest
	require.NoError(t, db.First(&first, "identity_id = ?::uuid", identityID).Error)
	require.Equal(t, firstEmail, first.RequestedEmailAddress)

	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-retry", identityID, previousEmail, "", retryEmail, false))
	var replacement model.AccountEmailChangeRequest
	require.NoError(t, db.First(&replacement, "identity_id = ?::uuid", identityID).Error)
	require.NotEqual(t, first.ID, replacement.ID)
	require.Equal(t, retryEmail, replacement.RequestedEmailAddress)

	// If the replacement also never reaches Kratos, only that orphan is
	// removed after the bounded pre-persist grace period.
	lifecycle.now = func() time.Time {
		return replacement.CreatedAt.Add(accountEmailChangeProofGrace + time.Second)
	}
	require.NoError(t, lifecycle.ReconcileRequest(ctx, replacement.ID))
	require.ErrorIs(t, db.First(&model.AccountEmailChangeRequest{}, "id = ?", replacement.ID).Error, gorm.ErrRecordNotFound)
	applied, err := identity.GetIdentity(ctx, identityID)
	require.NoError(t, err)
	require.Equal(t, previousEmail, applied.CurrentEmail())
	require.Empty(t, applied.PendingEmail())
}

func TestAccountEmailChangeSettingsVerificationRequiresExactUnverifiedAddressIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	identityID := integrationTestUUID()
	cleanupAccountEmailChangeFixtures(t, db, identityID)
	previousEmail := "old-" + identityID + "@example.test"
	requestedEmail := "new-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: previousEmail})
	identity := newAccountEmailChangeIdentityFixture(t, db, identityID, previousEmail)
	lifecycle := NewAccountEmailChangeLifecycle(db, identity, &accountEmailChangePublisherFixture{}, memberEmailProjectionIntegration{})
	require.NoError(t, lifecycle.StageOrCancel(
		t.Context(), "settings-flow", identityID, previousEmail, "", requestedEmail, false,
	))

	authorized, err := lifecycle.AuthorizeSettingsGeneratedVerification(
		t.Context(), identityID, requestedEmail,
	)
	require.NoError(t, err)
	require.False(t, authorized, "an active row alone must not authorize courier fallback")

	identity.mu.Lock()
	identity.identity.Traits["pending_email"] = requestedEmail
	identity.mu.Unlock()
	authorized, err = lifecycle.AuthorizeSettingsGeneratedVerification(
		t.Context(), identityID, requestedEmail,
	)
	require.NoError(t, err)
	require.False(t, authorized, "pending trait without an exact unverified address is insufficient")

	setPendingAccountEmail(identity, requestedEmail, false)
	authorized, err = lifecycle.AuthorizeSettingsGeneratedVerification(
		t.Context(), identityID, requestedEmail,
	)
	require.NoError(t, err)
	require.True(t, authorized)

	authorized, err = lifecycle.AuthorizeSettingsGeneratedVerification(
		t.Context(), identityID, "different@example.test",
	)
	require.NoError(t, err)
	require.False(t, authorized)

}

func TestAccountEmailChangeConfirmUncertaintyMayDuplicateStableCommandIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	ctx := t.Context()
	identityID := integrationTestUUID()
	cleanupAccountEmailChangeFixtures(t, db, identityID)
	previousEmail := "old-" + identityID + "@example.test"
	requestedEmail := "new-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: previousEmail})
	identity := newAccountEmailChangeIdentityFixture(t, db, identityID, previousEmail)
	publisher := &accountEmailChangePublisherFixture{acceptThenErrorOnce: true}
	lifecycle := NewAccountEmailChangeLifecycle(db, identity, publisher, memberEmailProjectionIntegration{})

	require.NoError(t, lifecycle.StageOrCancel(ctx, "settings-flow", identityID, previousEmail, "", requestedEmail, false))
	var request model.AccountEmailChangeRequest
	require.NoError(t, db.First(&request, "identity_id = ?::uuid", identityID).Error)
	setPendingAccountEmail(identity, requestedEmail, true)

	err := lifecycle.VerifyAndReconcile(ctx, "verification-flow", identityID, previousEmail, requestedEmail)
	require.ErrorContains(t, err, "confirm response lost")
	require.NoError(t, lifecycle.ReconcileRequest(ctx, request.ID))
	require.Len(t, publisher.jobs, 2)
	require.Equal(t, publisher.jobs[0].GetMessageId(), publisher.jobs[1].GetMessageId())
	require.Equal(t, "account-email-change:"+request.ID, publisher.jobs[0].GetMessageId())
	require.ErrorIs(t, db.First(&model.AccountEmailChangeRequest{}, "id = ?", request.ID).Error, gorm.ErrRecordNotFound)
}

func TestAccountEmailChangeReconcileScansPastFailingFirstBatchIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	ctx := t.Context()
	identityIDs := make([]string, 0, accountEmailChangeBatchSize+1)
	fixture := &accountEmailChangeMultiIdentityFixture{
		identities: make(map[string]*auth.Identity),
		errors:     make(map[string]error),
	}
	baseTime := time.Now().UTC().Add(-accountEmailChangeProofGrace - time.Minute)
	var lastRequest model.AccountEmailChangeRequest
	for i := 0; i < accountEmailChangeBatchSize+1; i++ {
		identityID := integrationTestUUID()
		identityIDs = append(identityIDs, identityID)
		previousEmail := "old-" + identityID + "@example.test"
		requestedEmail := "new-" + identityID + "@example.test"
		testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
			ID:    identityID,
			Email: previousEmail,
		})
		memberID := seedActiveMemberEmailPair(t, db, identityID, previousEmail)
		request := model.AccountEmailChangeRequest{
			ID:                    uuid.NewString(),
			MemberID:              memberID,
			IdentityID:            identityID,
			PreviousEmailAddress:  previousEmail,
			RequestedEmailAddress: requestedEmail,
			CreatedAt:             baseTime.Add(time.Duration(i) * time.Millisecond),
		}
		require.NoError(t, db.Create(&request).Error)
		if i < accountEmailChangeBatchSize {
			fixture.errors[identityID] = errors.New("identity backend unavailable")
		} else {
			lastRequest = request
		}
	}
	cleanupAccountEmailChangeFixtures(t, db, identityIDs...)

	lifecycle := NewAccountEmailChangeLifecycle(db, fixture, &accountEmailChangePublisherFixture{}, memberEmailProjectionIntegration{})
	require.NoError(t, lifecycle.Reconcile(ctx))

	require.ErrorIs(
		t,
		db.First(&model.AccountEmailChangeRequest{}, "id = ?", lastRequest.ID).Error,
		gorm.ErrRecordNotFound,
		"a failing first page must not starve later requests",
	)
	var remaining int64
	require.NoError(t, db.Model(&model.AccountEmailChangeRequest{}).
		Where("identity_id IN ?", identityIDs).
		Count(&remaining).Error)
	require.EqualValues(t, accountEmailChangeBatchSize, remaining)
}

func newAccountEmailChangeIdentityFixture(t *testing.T, db *gorm.DB, identityID, emailAddress string) *accountEmailChangeIdentityFixture {
	memberID := seedActiveMemberEmailPair(t, db, identityID, emailAddress)
	return &accountEmailChangeIdentityFixture{identity: &auth.Identity{
		ID: identityID, ExternalID: memberID,
		Traits: map[string]interface{}{
			"email": emailAddress,
		},
		VerifiableAddresses: []auth.VerifiableAddress{{
			Value:    emailAddress,
			Via:      "email",
			Verified: true,
		}},
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{emailAddress}},
		},
	}}
}

func setPendingAccountEmail(fixture *accountEmailChangeIdentityFixture, emailAddress string, verified bool) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.identity.Traits["pending_email"] = emailAddress
	fixture.identity.VerifiableAddresses = append(fixture.identity.VerifiableAddresses, auth.VerifiableAddress{
		Value:    emailAddress,
		Via:      "email",
		Verified: verified,
	})
}

func cleanupAccountEmailChangeFixtures(t *testing.T, db *gorm.DB, identityIDs ...string) {
	t.Helper()
	t.Cleanup(func() {
		require.NoError(t, db.Where("identity_id IN ?", identityIDs).Delete(&model.AccountEmailChangeRequest{}).Error)
		require.NoError(t, db.Where("account_identity_id IN ?", identityIDs).Delete(&model.Member{}).Error)
		require.NoError(t, db.Exec("DELETE FROM kratos.identities WHERE id IN ?", identityIDs).Error)
	})
}
