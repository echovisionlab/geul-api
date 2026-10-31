//go:build integration

package member

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type personalDataAccessWriterStub struct {
	records []sharedtelemetry.SecurityAccessRecord
	err     error
}

func (writer *personalDataAccessWriterStub) AppendSecurityAccess(
	_ context.Context,
	record sharedtelemetry.SecurityAccessRecord,
) error {
	writer.records = append(writer.records, record)
	return writer.err
}

func (*personalDataAccessWriterStub) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return nil
}

func TestMemberPersonalDataAccessPersistsExactOperationScopesIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	spiceDB := testutil.IntegrationSpiceDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	admin := seedMemberAdminListPair(
		t, db, spiceDB, "Access admin", memberAdminListEmail("access-admin"),
		policyv1.Role.Admin(), false, false, now,
	)
	targetEmail := memberAdminListEmail("access-target")
	target := seedMemberAdminListPair(
		t, db, spiceDB, "Access target", targetEmail,
		policyv1.Role.User(), false, false, now,
	)
	writer := apitelemetry.NewDurableWriter(db)
	service := NewConnectHandler(newTestService(t, Deps{
		DB:      db,
		SpiceDB: spiceDB,
		Identity: &fakeIdentityManager{identity: &auth.Identity{
			ID:         target.identityID,
			ExternalID: target.memberID,
			Traits:     map[string]interface{}{"email": targetEmail},
		}},
		Accounts:      integrationAccountSummaryReader{},
		AccountEmails: integrationAccountEmailProjection{},
		Audit:         writer,
	}))

	_, err := service.ListMembersAdmin(
		personalAccessAdminContext(t, admin),
		connect.NewRequest(&managev1.ListMembersAdminRequest{}),
	)
	require.NoError(t, err)
	_, err = service.GetMember(
		personalAccessAdminContext(t, admin),
		connect.NewRequest(&managev1.GetMemberRequest{MemberId: target.memberID}),
	)
	require.NoError(t, err)

	type storedAccess struct {
		Action        string
		ActorMemberID string
		RequestID     string
		SourceIP      string
		SubjectType   string
		SubjectID     string
		AccessKind    string
		DataCategory  string
	}
	var records []storedAccess
	require.NoError(t, db.Raw(`
		SELECT action, actor_member_id::text AS actor_member_id,
		       request_id::text AS request_id, host(source_ip) AS source_ip,
		       attributes->>'subject_type' AS subject_type,
		       attributes->>'subject_id' AS subject_id,
		       attributes->>'access_kind' AS access_kind,
		       attributes->>'data_category' AS data_category
		FROM security_access
		WHERE action = 'personal_data.accessed' AND actor_member_id = ?::uuid
		ORDER BY occurred_at, access_id
	`, admin.memberID).Scan(&records).Error)
	require.Len(t, records, 2)

	wantScopes := map[string]int{
		"member_collection:1:member_administration":            1,
		"member:" + target.memberID + ":member_administration": 1,
	}
	requestIDs := make(map[string]struct{}, len(records))
	for _, record := range records {
		require.Equal(t, string(sharedtelemetry.SecurityPersonalDataAccessed), record.Action)
		require.Equal(t, admin.memberID, record.ActorMemberID)
		require.Equal(t, "198.51.100.23", record.SourceIP)
		require.Equal(t, "read", record.AccessKind)
		require.NotContains(t, strings.Join(
			[]string{record.SubjectType, record.SubjectID, record.DataCategory}, ":",
		), "private-value@example.test")
		wantScopes[record.SubjectType+":"+record.SubjectID+":"+record.DataCategory]--
		requestIDs[record.RequestID] = struct{}{}
	}
	for scope, remaining := range wantScopes {
		require.Zero(t, remaining, scope)
	}
	require.Len(t, requestIDs, 2)
}

func TestMemberPersonalDataAccessFailsClosedWhenAppendFailsIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	spiceDB := testutil.IntegrationSpiceDB(t)
	admin := seedMemberAdminListPair(
		t, db, spiceDB, "Fail closed admin", memberAdminListEmail("fail-closed"),
		policyv1.Role.Admin(), false, false, time.Now().UTC(),
	)
	writer := &personalDataAccessWriterStub{err: errors.New("audit storage unavailable")}
	service := NewConnectHandler(newTestService(t, Deps{
		DB:       db,
		SpiceDB:  spiceDB,
		Accounts: integrationAccountSummaryReader{},
		Audit:    writer,
	}))

	response, err := service.ListMembersAdmin(
		personalAccessAdminContext(t, admin),
		connect.NewRequest(&managev1.ListMembersAdminRequest{}),
	)
	require.Nil(t, response)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.Len(t, writer.records, 1)
	require.Equal(t, "member_collection", writer.records[0].SubjectType)
}

func personalAccessAdminContext(t *testing.T, member memberAdminListPair) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("198.51.100.23")
	require.NoError(t, err)
	ctx := sharedtelemetry.WithRequestContext(t.Context(), requestContext)
	return auth.WithUser(ctx, &auth.UserInfo{
		IdentityID:    auth.IdentityID(member.identityID),
		MemberID:      auth.MemberID(member.memberID),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
		Onboarded:     true,
	})
}

var _ securityaccess.Appender = (*personalDataAccessWriterStub)(nil)
