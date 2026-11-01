package account

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newAccountErrorTestDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, sqlDB.Close()) })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return db, mock
}

type deletionSnapshotErrorLifecycle struct {
	MemberDeletionLifecycle
	err error
}

func (s deletionSnapshotErrorLifecycle) NotificationSnapshot(context.Context, *gorm.DB, string, string) (MemberDeletionNotification, error) {
	return MemberDeletionNotification{}, s.err
}

type deletionPrimaryEmailProjection struct {
	memberEmailProjectionStub
	readErr error
}

func (s deletionPrimaryEmailProjection) PrimaryEmail(context.Context, *gorm.DB, string, string) (string, error) {
	return "member@example.test", s.readErr
}

func TestRequestDeletionPreservesLinkResolutionFailure(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "database failure"
		if missing {
			name = "missing active link"
		}
		t.Run(name, func(t *testing.T) {
			db, mock := newAccountErrorTestDB(t)
			lookupErr := errors.New("active link database unavailable")
			query := mock.ExpectQuery(`SELECT member.id::text AS member_id`)
			want := connect.CodeInternal
			if missing {
				query.WillReturnRows(sqlmock.NewRows([]string{"member_id", "identity_id"}))
				want = connect.CodeFailedPrecondition
			} else {
				query.WillReturnError(lookupErr)
			}
			service := &Service{db: db, memberDeletion: deletionSnapshotErrorLifecycle{}}
			result, err := requestDeletionFor(t.Context(), service, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
			require.Nil(t, result)
			require.Equal(t, want, errs.Wrap(err).Code())
			if !missing {
				require.ErrorIs(t, err, lookupErr)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestRequestDeletionPreservesRecipientAndSnapshotFailures(t *testing.T) {
	for _, stage := range []string{"recipient", "snapshot", "missing snapshot"} {
		t.Run(stage, func(t *testing.T) {
			db, mock := newAccountErrorTestDB(t)
			memberID := "11111111-1111-4111-8111-111111111111"
			identityID := "22222222-2222-4222-8222-222222222222"
			mock.ExpectQuery(`SELECT member.id::text AS member_id`).WillReturnRows(sqlmock.NewRows([]string{"member_id", "identity_id", "onboarded"}).AddRow(memberID, identityID, true))
			mock.ExpectBegin()
			mock.ExpectCommit()
			identity := &auth.Identity{ID: identityID, ExternalID: memberID, State: auth.KratosStateActive,
				Traits: structured.Fields{"email": "member@example.test"}, Credentials: map[string]auth.Credential{"code": {Type: "code", Identifiers: []string{"member@example.test"}}}}
			lookupErr := errors.New(stage + " database unavailable")
			projection := deletionPrimaryEmailProjection{}
			lifecycle := deletionSnapshotErrorLifecycle{err: lookupErr}
			want := connect.CodeInternal
			if stage == "recipient" {
				projection.readErr = lookupErr
			}
			if stage == "missing snapshot" {
				lifecycle.err = gorm.ErrRecordNotFound
				want = connect.CodeFailedPrecondition
			}
			service := &Service{db: db, identity: accountSecurityIdentityManager{identity: identity}, memberEmails: projection, memberDeletion: lifecycle}
			result, err := requestDeletionFor(t.Context(), service, memberID, identityID)
			require.Nil(t, result)
			require.Equal(t, want, errs.Wrap(err).Code())
			if want == connect.CodeInternal {
				require.ErrorIs(t, err, lookupErr)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

type deletionRecipientIdentityFailure struct {
	accountSecurityIdentityManager
	calls     int
	lookupErr error
}

func (m *deletionRecipientIdentityFailure) GetIdentityWithIncludeCredential(ctx context.Context, identityID, credential string) (*auth.Identity, error) {
	m.calls++
	if m.calls > 2 {
		return nil, m.lookupErr
	}
	return m.accountSecurityIdentityManager.GetIdentityWithIncludeCredential(ctx, identityID, credential)
}

func TestRequestDeletionDoesNotExposeRecipientIdentityFailureBody(t *testing.T) {
	db, mock := newAccountErrorTestDB(t)
	memberID := "11111111-1111-4111-8111-111111111111"
	identityID := "22222222-2222-4222-8222-222222222222"
	mock.ExpectQuery(`SELECT member.id::text AS member_id`).WillReturnRows(sqlmock.NewRows([]string{"member_id", "identity_id", "onboarded"}).AddRow(memberID, identityID, true))
	mock.ExpectBegin()
	mock.ExpectCommit()
	identity := &auth.Identity{ID: identityID, ExternalID: memberID, State: auth.KratosStateActive,
		Traits: structured.Fields{"email": "member@example.test"}, Credentials: map[string]auth.Credential{"code": {Type: "code", Identifiers: []string{"member@example.test"}}}}
	lookupErr := &auth.KratosHTTPError{StatusCode: http.StatusServiceUnavailable, Body: "private-upstream-response session_token=private-token"}
	manager := &deletionRecipientIdentityFailure{accountSecurityIdentityManager: accountSecurityIdentityManager{identity: identity}, lookupErr: lookupErr}
	service := &Service{db: db, identity: manager, memberEmails: deletionPrimaryEmailProjection{}, memberDeletion: deletionSnapshotErrorLifecycle{}}
	result, err := requestDeletionFor(t.Context(), service, memberID, identityID)
	require.Nil(t, result)
	require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
	require.ErrorIs(t, err, lookupErr)
	require.NotContains(t, err.Error(), "private-upstream-response")
	require.NotContains(t, err.Error(), "private-token")
	require.NoError(t, mock.ExpectationsWereMet())
}
