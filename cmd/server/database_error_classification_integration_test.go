//go:build integration

package main

import (
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

// The server's GORM/pgx connection is what classifies these driver errors in
// production, so the PostgreSQL error context is exercised for real here.
func TestDatabaseUUIDErrorsSeparateClientInputFromStoredDataIntegration(t *testing.T) {
	t.Parallel()
	postgres := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	})
	db := postgres.DB.WithContext(t.Context())
	var ids []string

	for name, query := range map[string]string{
		"uuid column parameter": `SELECT id::text FROM member WHERE id = ?`,
		"cast parameter":        `SELECT ?::uuid::text`,
	} {
		err := db.Raw(query, "not-a-uuid").Scan(&ids).Error
		require.Error(t, err, name)
		classified := errs.Wrap(err)
		require.Equal(t, connect.CodeInvalidArgument, classified.Code(), "%s: %v", name, err)
		require.Equal(t, "request contains an invalid UUID", classified.Message())
	}

	// A stored value the server cannot decode is a server fault.
	storedID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, nickname, onboarded, available_emails, bio)
		VALUES (?::uuid, ?, TRUE, '{}'::text[], 'stored-not-a-uuid')
	`, storedID, "decode-"+storedID).Error)
	for name, query := range map[string]string{
		"stored text cast":   `SELECT bio::uuid::text FROM member WHERE id = ?::uuid`,
		"stored text filter": `SELECT id::text FROM member WHERE id = ?::uuid AND bio::uuid IS NOT NULL`,
	} {
		err := db.Raw(query, storedID).Scan(&ids).Error
		require.Error(t, err, name)
		classified := errs.Wrap(err)
		require.Equal(t, connect.CodeInternal, classified.Code(), "%s: %v", name, err)
		require.Equal(t, slog.LevelError, errs.OperationLogLevel(classified))
	}
}
