package series

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/og"
)

func TestProjectionCompletionUsesTargetRowExistence(t *testing.T) {
	db := seriesProjectionUnitDB(t)
	entityID, assetID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO series_translation (
		entity_id, locale, updated_at
	) VALUES (?, 'ko', CURRENT_TIMESTAMP)`, entityID).Error)
	seedSeriesProjectionReadyAsset(t, db, assetID)

	err := NewProjection().Complete(t.Context(), db, og.Target{
		EntityType: "series", EntityID: entityID, Locale: new("ko"), Kind: "locale",
	}, assetID, time.Now().UTC(), "https://cdn.example.com")
	require.NoError(t, err)
	var stored *string
	require.NoError(t, db.Table("series_translation").Select("og_asset_id").Where("entity_id = ? AND locale = 'ko'", entityID).Scan(&stored).Error)
	require.NotNil(t, stored)
	require.Equal(t, assetID, *stored)
}

func TestProjectionCompletionRejectsMissingTargetRow(t *testing.T) {
	db := seriesProjectionUnitDB(t)
	assetID := uuid.NewString()
	seedSeriesProjectionReadyAsset(t, db, assetID)
	err := NewProjection().Complete(t.Context(), db, og.Target{
		EntityType: "series", EntityID: uuid.NewString(), Locale: new("ko"), Kind: "locale",
	}, assetID, time.Now().UTC(), "https://cdn.example.com")
	require.ErrorIs(t, err, og.ErrTranslationTargetMissing)
}

func seriesProjectionUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE series_translation (
		entity_id text, locale text, og_asset_id text, updated_at datetime,
		PRIMARY KEY (entity_id, locale)
	);
	CREATE TABLE public_asset (
		id text PRIMARY KEY, source_file_id text, kind text, object_key text,
		extension text, mime_type text, file_size integer, sha256 blob,
		disposition text, download_filename text, status text, ready_at datetime,
		delete_requested_at datetime, deleted_at datetime, failed_at datetime,
		failure_reason text, created_at datetime, updated_at datetime
	);
	CREATE TABLE public_asset_binding (
		asset_id text, owner_type text, owner_id text, binding_key text,
		source_file_id text, created_at datetime, updated_at datetime,
		PRIMARY KEY (owner_type, owner_id, binding_key)
	)`).Error)
	return db
}

func seedSeriesProjectionReadyAsset(t *testing.T, db *gorm.DB, assetID string) {
	t.Helper()
	digest := make([]byte, 32)
	require.NoError(t, db.Exec(`INSERT INTO public_asset (
		id, kind, object_key, extension, mime_type, file_size, sha256,
		disposition, status, ready_at, created_at, updated_at
	) VALUES (?, 'og', ?, 'webp', 'image/webp', 1, ?, 'inline', 'ready', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		assetID, "asset/"+assetID+".webp", digest,
	).Error)
}
