//go:build integration

package mutation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// postgresNotes is the PostgreSQL notes Store: SQL, the unique constraint
// and the row lock live here, behind the port.
type postgresNotes struct{ tx *gorm.DB }

type noteRow struct {
	Name     string `gorm:"column:name;primaryKey"`
	Revision int    `gorm:"column:revision"`
}

func (noteRow) TableName() string { return "contract_note" }

func (s postgresNotes) Insert(ctx context.Context, name string) error {
	err := s.tx.WithContext(ctx).Create(&noteRow{Name: name, Revision: 1}).Error
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return errDuplicateNote
	}
	return err
}

func (s postgresNotes) Lock(ctx context.Context, name string) (int, error) {
	var row noteRow
	err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("name = ?", name).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, errNoteNotFound
	}
	return row.Revision, err
}

func (s postgresNotes) Bump(ctx context.Context, name string, expected int) error {
	result := s.tx.WithContext(ctx).Model(&noteRow{}).Where("name = ? AND revision = ?", name, expected).
		Update("revision", gorm.Expr("revision + 1"))
	if result.Error == nil && result.RowsAffected == 0 {
		return errRevisionChanged
	}
	return result.Error
}

func TestPostgresNotesKeepTheNotesContractIntegration(t *testing.T) {
	t.Parallel()
	mutationtest.RunContract(t, func(t *testing.T) mutationtest.Harness[notes] {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		require.NoError(t, db.Exec(`CREATE TABLE contract_note (name text PRIMARY KEY, revision integer NOT NULL)`).Error)
		units := mutation.Postgres(db, func(tx *gorm.DB) notes { return postgresNotes{tx: tx} })
		return mutationtest.PostgresHarness(db, units)
	}, notesContract)
}
