//go:build integration

package main

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestPostgresPGMQReadinessCheckIntegration(t *testing.T) {
	postgres := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	})
	check := newPostgresPGMQReadinessCheck(postgres.SQLDB)

	require.NoError(t, check(t.Context()))
}
