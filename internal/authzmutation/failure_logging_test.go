package authzmutation

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMutationPanicLogsCauseAndStackBeforeRepanicking(t *testing.T) {
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
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "authorization_mutation_panic", entry["reason"])
	require.Contains(t, entry["failure_cause"], "forced mutation panic")
	require.Contains(t, entry["panic_stack"], "runtime/debug.Stack")
}
