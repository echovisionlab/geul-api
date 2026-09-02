package sitemapadapter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	sitemapdomain "github.com/echovisionlab/geul-api/internal/sitemap"
)

func TestPostgresStoreNilSnapshotIsNoop(t *testing.T) {
	t.Parallel()

	store := NewPostgresStore(newDryRunDB(t))
	written, err := store.SaveSnapshot(context.Background(), "ignored", nil)
	require.NoError(t, err)
	require.False(t, written)
}

func TestSnapshotUpsertSkipsSemanticallyUnchangedContent(t *testing.T) {
	t.Parallel()

	db := newDryRunDB(t)
	snapshot := &sitemapdomain.Snapshot{
		Content:     "<urlset>same</urlset>",
		ContentType: "application/xml; charset=utf-8",
		GeneratedAt: time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC),
	}

	sql := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return executeSnapshotUpsert(tx, "public:sitemap:v1:test", snapshot)
	})
	normalized := strings.Join(strings.Fields(sql), " ")
	require.Contains(t, normalized,
		"WHERE NOT EXISTS ( SELECT 1 FROM public.sitemap_snapshot WHERE key =",
	)
	require.Contains(t, normalized,
		"AND content IS NOT DISTINCT FROM",
	)
	require.Contains(t, normalized,
		"WHERE sitemap_snapshot.content IS DISTINCT FROM EXCLUDED.content OR sitemap_snapshot.content_type IS DISTINCT FROM EXCLUDED.content_type",
	)
	require.NotContains(t, normalized, "sitemap_snapshot.generated_at IS DISTINCT")
}

func TestNewPostgresStoreRequiresDatabase(t *testing.T) {
	t.Parallel()

	require.Panics(t, func() { NewPostgresStore(nil) })
	require.NotNil(t, NewPostgresStore(newDryRunDB(t)))
}

func newDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=localhost user=geul dbname=geul sslmode=disable",
	}), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	require.NoError(t, err)
	return db
}
