//go:build integration

package member

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func auditedMemberContext(t *testing.T, identityID, memberID string) context.Context {
	t.Helper()
	request, err := sharedtelemetry.NewPublicRequestContext("192.0.2.77")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), request), &auth.UserInfo{
		SessionID: auth.SessionID(integrationTestUUID()), IdentityID: auth.IdentityID(identityID),
		MemberID: auth.MemberID(memberID), Authenticated: true, Onboarded: true,
	})
}

type failingDomainAuditAppender struct{}

func (failingDomainAuditAppender) AppendDomainAuditInTransaction(context.Context, *gorm.DB, sharedtelemetry.AuditRecord) error {
	return errors.New("audit unavailable")
}

func requireAccountIdentitySubject(t *testing.T, identityID string) auth.AccountIdentitySubject {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	return subject
}

func syncIntegrationGlobalRole(
	t *testing.T, spiceDB *auth.SpiceDBClient, identityID string, role policyv1.RoleID,
) {
	t.Helper()
	subject := requireAccountIdentitySubject(t, identityID)
	_, err := spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, role)
	require.NoError(t, err)
}

func integrationTestUUID() string { return uuid.NewString() }

type integrationAccountEmailProjection struct{}

// AdminDetails supplies the Account projection at the Member admin boundary.
// The account-email lifecycle tests exercise candidate construction separately;
// Member list tests only need a non-nil projection to keep their focus on
// Member filtering and pagination.
func (integrationAccountEmailProjection) AdminDetails(
	context.Context, *auth.Identity,
) (*managev1.AccountAdminDetails, error) {
	return &managev1.AccountAdminDetails{}, nil
}

func (integrationAccountEmailProjection) ResolveDelivery(
	ctx context.Context, db *gorm.DB, identity auth.IdentityGetter, identityID string,
) (string, string, string, error) {
	result, reason, err := account.ResolveMemberPrimaryEmailForIdentity(
		ctx, db, integrationMemberEmailProjection{}, identity, identityID,
	)
	if err != nil || result == nil || result.Identity == nil {
		return "", "", reason, err
	}
	return result.Email, result.Identity.ExternalID, reason, nil
}

func (integrationAccountEmailProjection) PrepareRegistration(
	ctx context.Context,
	identityManager auth.IdentityGetter,
	identityID string,
	requestedEmail string,
) (*auth.Identity, string, []string, error) {
	identity, err := account.LoadIdentityWithEmailCredentials(ctx, identityManager, identityID)
	if err != nil || identity == nil {
		if err == nil {
			err = fmt.Errorf("identity was not returned")
		}
		return nil, "", nil, err
	}
	if identity.ID != identityID {
		return nil, "", nil, fmt.Errorf("identity lookup returned a different identity")
	}
	if _, err := uuidutil.ParseCanonical(identity.ID, "identity_id"); err != nil {
		return nil, "", nil, err
	}
	if identity.State != auth.KratosStateActive || identity.IsBanned() {
		return nil, "", nil, fmt.Errorf("registration identity is not active")
	}
	normalized, ok := account.NormalizeAccountEmailInput(requestedEmail)
	if !ok || normalized != emailutil.NormalizeAddressForDelivery(identity.CurrentEmail()) {
		return nil, "", nil, fmt.Errorf("registration email does not match the exact identity")
	}
	rows := account.ProjectedAccountEmailRows(identity, account.ResolveAccountEmailProviderCandidates(ctx, identity.Credentials))
	current := account.FindAccountEmailProjection(rows, normalized)
	if current == nil || !current.UsableForDelivery {
		return nil, "", nil, fmt.Errorf("registration email is not proven by the exact identity")
	}
	if identity.ExternalID != "" {
		if _, err := uuidutil.ParseCanonical(identity.ExternalID, "identity.external_id"); err != nil {
			return nil, "", nil, err
		}
		if identity.ExternalID == identityID {
			return nil, "", nil, fmt.Errorf("identity_id and member_id must be distinct")
		}
	}
	emails := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.UsableForDelivery {
			emails = append(emails, row.NormalizedEmail)
		}
	}
	sort.Strings(emails)
	return identity, current.NormalizedEmail, emails, nil
}

func (integrationAccountEmailProjection) SyncMemberEmailProjection(
	ctx context.Context,
	db *gorm.DB,
	identityManager auth.IdentityGetter,
	identityID string,
	identity *auth.Identity,
) error {
	var candidates []account.AccountEmailProviderCandidate
	if identity != nil {
		candidates = account.ResolveAccountEmailProviderCandidates(ctx, identity.Credentials)
	}
	_, err := account.NewAccountEmailService(db, identityManager, integrationMemberEmailProjection{}).
		SyncMemberEmailProjection(ctx, identityID, identity, candidates)
	return err
}

type integrationMemberEmailProjection struct{}

func (integrationMemberEmailProjection) PrimaryEmail(ctx context.Context, db *gorm.DB, memberID, identityID string) (string, error) {
	return PrimaryEmail(ctx, db, memberID, identityID)
}

func (integrationMemberEmailProjection) SyncEmailProjection(ctx context.Context, db *gorm.DB, memberID, identityID, primaryEmail string, availableEmails []string) error {
	return SyncEmailProjection(ctx, db, memberID, identityID, primaryEmail, availableEmails)
}

type integrationAccountSummaryReader struct{}

func (integrationAccountSummaryReader) SessionSummaryForMember(
	ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, memberID string,
) (*managev1.AccountSummary, error) {
	return account.SessionSummaryForMember(ctx, db, spiceDB, memberID)
}

func (integrationAccountSummaryReader) SummaryForMember(
	ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, memberID string,
) (*managev1.AccountSummary, error) {
	return account.SummaryForMember(ctx, db, spiceDB, memberID)
}

func (integrationAccountSummaryReader) SummariesForMembers(
	ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, memberIDs []string,
) (map[string]*managev1.AccountSummary, error) {
	return account.SummariesForMembers(ctx, db, spiceDB, memberIDs)
}

type integrationDirectRoleTransition struct{}

func (integrationDirectRoleTransition) Transition(
	subject auth.AccountIdentitySubject,
	desired policyv1.RoleID,
	previous policyv1.RoleID,
	previousFound bool,
) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	apply, err := account.RoleReplacementMutations(subject, desired)
	if err != nil {
		return nil, nil, err
	}
	compensate, err := account.RoleRestoreMutations(subject, previous, previousFound)
	if err != nil {
		return nil, nil, err
	}
	return apply, compensate, nil
}

func integrationMemberProvisioner(db *gorm.DB, identity memberProvisioningIdentity, spiceDB *auth.SpiceDBClient) *MemberProvisioner {
	return NewMemberProvisioner(db, identity, spiceDB, integrationAccountEmailProjection{}, integrationDirectRoleTransition{})
}
