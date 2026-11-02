package filemediapostgres

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestFaviconCommitDriverPreservesFailurePhaseAndRollback(t *testing.T) {
	for _, phase := range []string{"begin", "change", "commit", "success"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			sqlDB, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true})
			require.NoError(t, err)
			injected, rollback := errors.New("phase failed"), errors.New("rollback failed")
			begin := mock.ExpectBegin()
			wantStage := domain.FaviconCommitted
			switch phase {
			case "begin":
				begin.WillReturnError(injected)
				wantStage = domain.FaviconBeginFailed
			case "change":
				mock.ExpectRollback().WillReturnError(rollback)
				wantStage = domain.FaviconChangeFailed
			case "commit":
				mock.ExpectCommit().WillReturnError(injected)
				wantStage = domain.FaviconCommitFailed
			default:
				mock.ExpectCommit()
			}
			called := false
			result := NewFaviconCommits(db, Ports{}).Commit(t.Context(), func(store domain.Store) error {
				called = true
				require.NotNil(t, store)
				if phase == "change" {
					return injected
				}
				return nil
			})
			require.Equal(t, wantStage, result.Stage)
			require.Equal(t, phase != "begin", called)
			if phase == "success" {
				require.NoError(t, result.Err)
			} else {
				require.ErrorIs(t, result.Err, injected)
			}
			if phase == "change" {
				require.ErrorIs(t, result.Err, rollback)
			}
			require.NoError(t, mock.ExpectationsWereMet())
			mock.ExpectClose()
		})
	}
}
