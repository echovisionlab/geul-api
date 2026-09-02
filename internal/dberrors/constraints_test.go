package dberrors

import (
	"errors"
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestIsForeignKeyViolationUsesStableDriverErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "gorm translated foreign key violation", err: gorm.ErrForeignKeyViolated, want: true},
		{name: "wrapped gorm translated foreign key violation", err: fmt.Errorf("delete tag: %w", gorm.ErrForeignKeyViolated), want: true},
		{name: "postgres foreign key violation", err: &pgconn.PgError{Code: "23503"}, want: true},
		{name: "wrapped postgres foreign key violation", err: fmt.Errorf("delete tag: %w", &pgconn.PgError{Code: "23503"}), want: true},
		{name: "postgres unique violation", err: &pgconn.PgError{Code: "23505"}, want: false},
		{name: "sqlite foreign key constraint", err: sqliteContractError{code: 787, message: "constraint failed"}, want: true},
		{name: "sqlite restricted foreign key delete", err: sqliteContractError{code: 1811, message: "constraint failed: FOREIGN KEY constraint failed (1811)"}, want: true},
		{name: "wrapped sqlite restricted foreign key delete", err: fmt.Errorf("delete tag: %w", sqliteContractError{code: 1811, message: "constraint failed: FOREIGN KEY constraint failed (1811)"}), want: true},
		{name: "sqlite unrelated trigger constraint", err: sqliteContractError{code: 1811, message: "constraint failed: blocked by trigger"}, want: false},
		{name: "untyped foreign key text", err: errors.New("operation failed because a foreign key constraint failed elsewhere"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, IsForeignKeyViolation(test.err))
		})
	}
}

func TestSQLiteForeignKeyViolationUsesSupportedDriverContracts(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	require.NoError(t, db.Exec("PRAGMA foreign_keys = ON").Error)
	require.NoError(t, db.Exec("CREATE TABLE parent (id TEXT PRIMARY KEY)").Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE child (
			id TEXT PRIMARY KEY,
			parent_id TEXT NOT NULL REFERENCES parent(id) ON DELETE RESTRICT
		)
	`).Error)

	err = db.Exec("INSERT INTO child (id, parent_id) VALUES ('child', 'missing')").Error
	require.ErrorIs(t, err, gorm.ErrForeignKeyViolated)
	require.True(t, IsForeignKeyViolation(err))

	err = db.Transaction(func(tx *gorm.DB) error {
		return tx.Exec("INSERT INTO child (id, parent_id) VALUES ('child-in-transaction', 'missing')").Error
	})
	require.ErrorIs(t, err, gorm.ErrForeignKeyViolated)
	require.True(t, IsForeignKeyViolation(err))

	require.NoError(t, db.Exec("INSERT INTO parent (id) VALUES ('referenced')").Error)
	require.NoError(t, db.Exec("INSERT INTO child (id, parent_id) VALUES ('restrict-child', 'referenced')").Error)
	err = db.Exec("DELETE FROM parent WHERE id = 'referenced'").Error
	require.NotErrorIs(t, err, gorm.ErrForeignKeyViolated)
	require.True(t, IsForeignKeyViolation(err))
}

func TestIsUniqueViolation(t *testing.T) {
	require.False(t, IsUniqueViolation(nil))
	require.False(t, IsUniqueViolation(errors.New("database unavailable")))
	require.True(t, IsUniqueViolation(gorm.ErrDuplicatedKey))
	require.True(t, IsUniqueViolation(&pgconn.PgError{Code: "23505"}))
	require.True(t, IsUniqueViolation(errors.New("duplicate key value violates unique constraint")))
}

type sqliteContractError struct {
	code    int
	message string
}

func (err sqliteContractError) Error() string { return err.message }
func (err sqliteContractError) Code() int     { return err.code }
