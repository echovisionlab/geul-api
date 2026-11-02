//go:build integration

package authenticationpostgres_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	authenticationpostgres "github.com/echovisionlab/geul-api/internal/authentication/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestDeleteExpiredAuthCodeIssuancesIsBoundedIntegration(t *testing.T) {
	t.Parallel()
	const batchSize = 1000
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	now := time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC)
	require.NoError(t, db.Exec(`
		INSERT INTO public.auth_code_issuance (
			token, purpose, recipient_digest, subject_digest,
			client_ip_digest, issued_at, expires_at
		)
		SELECT
			lpad(to_hex(sequence), 64, '0'),
			'login_code',
			repeat('a', 64),
			repeat('b', 64),
			repeat('c', 64),
			?,
			?
		FROM generate_series(1, ?) AS generated(sequence)`,
		now.Add(-2*time.Minute),
		now.Add(-time.Minute),
		batchSize+1,
	).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO public.auth_code_issuance (
			token, purpose, recipient_digest, subject_digest,
			client_ip_digest, issued_at, expires_at
		) VALUES (?, 'login_code', ?, ?, ?, ?, ?)`,
		fmt.Sprintf("%064x", batchSize+2),
		fmt.Sprintf("%064x", 4),
		fmt.Sprintf("%064x", 5),
		fmt.Sprintf("%064x", 6),
		now,
		now.Add(time.Hour),
	).Error)

	deleted, err := authenticationpostgres.CleanupExpiredAuthCodeIssuances(t.Context(), db, now, batchSize)
	require.NoError(t, err)
	require.Equal(t, int64(batchSize), deleted)

	var expired int64
	require.NoError(t, db.Model(&struct{}{}).
		Table("public.auth_code_issuance").
		Where("expires_at <= ?", now).
		Count(&expired).Error)
	require.Equal(t, int64(1), expired)

	deleted, err = authenticationpostgres.CleanupExpiredAuthCodeIssuances(t.Context(), db, now, batchSize)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)

	var total int64
	require.NoError(t, db.Model(&struct{}{}).
		Table("public.auth_code_issuance").
		Count(&total).Error)
	require.Equal(t, int64(1), total)
}
