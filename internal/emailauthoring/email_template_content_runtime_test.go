package emailauthoring

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/DATA-DOG/go-sqlmock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestEmailTemplateDocumentLoadDistinguishesInvalidSessionFromLookupFailure(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "lookup failure"
		if missing {
			name = "invalid session"
		}
		t.Run(name, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			query := mock.ExpectQuery(`SELECT .*FROM kratos.sessions AS session`)
			lookupErr := errors.New("principal database unavailable")
			want := connect.CodeInternal
			if missing {
				query.WillReturnRows(sqlmock.NewRows([]string{"session_id", "identity_id", "member_id"}))
				want = connect.CodeUnauthenticated
			} else {
				query.WillReturnError(lookupErr)
			}
			err = requireEmailTemplateDocumentLoad(t.Context(), db, nil, nil,
				&intrav1.CollaborationPrincipal{SessionId: "11111111-1111-4111-8111-111111111111"}, "template")
			require.Equal(t, want, errs.Wrap(err).Code())
			if !missing {
				require.ErrorIs(t, err, lookupErr)
			}
			require.NoError(t, mock.ExpectationsWereMet())
			mock.ExpectClose()
		})
	}
}
