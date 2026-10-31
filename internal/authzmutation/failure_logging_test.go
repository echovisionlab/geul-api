package authzmutation

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The RPC and queue boundaries log a recovered panic once, with its stack;
// the mutation helper only re-raises it after rollback.
func TestMutationPanicRepanicsWithoutLogging(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	value := captureAuthorizationMutationPanic(func() {
		_, _ = Execute(context.Background(), db, &auth.SpiceDBClient{}, func(*gorm.DB, WriteRelationships) error { panic("forced mutation panic") })
	})
	require.Equal(t, "forced mutation panic", value)
	require.Empty(t, output.String())
}
