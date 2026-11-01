//go:build integration

package orytest

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	pgtest "github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

// OpenIsolatedSpiceDB returns a client for a fresh datastore with the generated
// schema and the fixed role hierarchy.
func OpenIsolatedSpiceDB(ctx context.Context, endpoint string) (string, *auth.SpiceDBClient, error) {
	token, err := pgtest.NewIsolationIdentifier("it-spicedb-")
	if err != nil {
		return "", nil, err
	}
	raw, err := NewIntegrationSpiceDBRawClient(endpoint, token)
	if err != nil {
		return "", nil, err
	}
	if err := WriteIntegrationSpiceDBSchema(ctx, raw); err != nil {
		return "", nil, errors.Join(err, raw.Close())
	}
	if err := raw.Close(); err != nil {
		return "", nil, fmt.Errorf("close SpiceDB schema client: %w", err)
	}
	client, err := auth.NewSpiceDBClient(endpoint, token, true)
	if err != nil {
		return "", nil, fmt.Errorf("create isolated SpiceDB client: %w", err)
	}
	if err := SeedIntegrationSpiceDBRoleGraph(ctx, client); err != nil {
		return "", nil, errors.Join(err, client.Close())
	}
	return token, client, nil
}
