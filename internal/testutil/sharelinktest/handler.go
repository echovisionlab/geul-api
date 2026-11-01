//go:build integration

// Package sharelinktest wires the Share Link management RPCs for integration
// tests of the domains that own link targets.
package sharelinktest

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	sharelinkpostgres "github.com/echovisionlab/geul-api/internal/sharelink/postgres"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
)

// NewHandler returns the Share Link management RPCs wired as in
// production, with durable audit.
func NewHandler(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient) *sharelink.ConnectHandler {
	t.Helper()
	links, err := sharelink.NewService(sharelink.Deps{
		Units: sharelinkpostgres.NewUnits(db), SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: apitelemetry.NewDurableWriter(db),
	})
	require.NoError(t, err)
	return sharelink.NewConnectHandler(links)
}
