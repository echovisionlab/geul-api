//go:build integration

package main

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/stretchr/testify/require"
)

func TestPostgresPGMQReadinessCheckIntegration(t *testing.T) {
	t.Parallel()
	postgres := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	})
	check := newPostgresPGMQReadinessCheck(postgres.SQLDB)

	require.NoError(t, check(t.Context()))
}
