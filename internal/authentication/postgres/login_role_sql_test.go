package authenticationpostgres_test

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/authentication/authenticationtest"
	authenticationpostgres "github.com/echovisionlab/geul-api/internal/authentication/postgres"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// The login role sync on the PostgreSQL units issues its statements in this
// order, and an unchanged role rolls back without hiding a failed rollback.
func TestUnchangedLoginRoleDoesNotHideRollbackFailure(t *testing.T) {
	const identityID, memberID = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	for _, rollbackFails := range []bool{false, true} {
		name := "successful rollback"
		if rollbackFails {
			name = "failed rollback"
		}
		t.Run(name, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, sqlDB.Close()) })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock\(`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtext`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`INSERT INTO auth_bootstrap_state`).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery(`SELECT COALESCE\(identity_id::text, ''\) FROM auth_bootstrap_state`).WillReturnRows(sqlmock.NewRows([]string{"identity_id"}).AddRow("another-identity"))
			rollbackErr := errors.New("database rollback failed")
			rollback := mock.ExpectRollback()
			if rollbackFails {
				rollback.WillReturnError(rollbackErr)
			}
			spiceDB := authenticationtest.NewAuthorizer(&authztest.Checker{}).SetRole(identityID, policyv1.Role.User())
			service, err := authentication.NewService(authentication.Deps{
				Units: authenticationpostgres.NewUnits(db), SpiceDB: spiceDB, Fence: identitystate.PostgresFence{},
				Audit: &audittest.Recorder{}, Roles: accountadapter.AccountDirectRoleTransition{}, Sessions: authenticationpostgres.NewSessions(db),
			})
			require.NoError(t, err)

			result, err := service.SyncLoginRole(t.Context(), identityID, memberID)

			if rollbackFails {
				require.ErrorIs(t, err, rollbackErr)
				require.ErrorContains(t, err, "login role is unchanged")
				require.Empty(t, result)
			} else {
				require.NoError(t, err)
				require.Equal(t, policyv1.Role.User(), result.Role)
			}
			require.Empty(t, spiceDB.Applied())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestCleanupExpiredAuthCodeIssuancesRejectsAMissingDatabaseOrBound(t *testing.T) {
	_, err := authenticationpostgres.CleanupExpiredAuthCodeIssuances(t.Context(), nil, time.Now(), 1)
	require.ErrorContains(t, err, "database is required")
	_, err = authenticationpostgres.CleanupExpiredAuthCodeIssuances(t.Context(), &gorm.DB{}, time.Now(), 0)
	require.ErrorContains(t, err, "limit must be positive")
}
