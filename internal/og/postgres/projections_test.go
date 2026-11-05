package ogpostgres

import (
	"testing"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	"github.com/echovisionlab/geul-api/internal/og"

	"connectrpc.com/connect"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const projectionTestCDN = "https://cdn.example.com"

func TestTranslationProjectionAnswersLocalelessTargetsPerFamily(t *testing.T) {
	db := projectionUnitDB(t)
	entityID, assetID := uuid.NewString(), uuid.NewString()
	seedProjectionReadyAsset(t, db, assetID)
	require.NoError(t, db.Exec("INSERT INTO page (id) VALUES (?)", entityID).Error)
	localeless := og.Target{EntityType: "page", EntityID: entityID, Kind: "entity"}

	rejected := NewTranslationProjection(TranslationProjectionSpec{EntityType: "page", EntityTitle: "Page", Localeless: LocalelessRejected})
	err := rejected.Complete(t.Context(), db, localeless, assetID, projectionTestCDN)
	require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))
	require.ErrorContains(t, err, "Page OG target locale is required")
	err = rejected.ReleasePending(t.Context(), db, localeless, projectionTestCDN)
	require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))

	missing := NewTranslationProjection(TranslationProjectionSpec{EntityType: "page", Localeless: LocalelessMissing})
	require.ErrorIs(t, missing.Complete(t.Context(), db, localeless, assetID, projectionTestCDN), gorm.ErrRecordNotFound)

	entityRow := NewTranslationProjection(TranslationProjectionSpec{EntityType: "page", Localeless: LocalelessEntityRow})
	require.NoError(t, entityRow.Complete(t.Context(), db, localeless, assetID, projectionTestCDN))
	require.Equal(t, assetID, projectionPointer(t, db, "page", entityID))
	require.Equal(t, []string{"og"}, projectionBindingKeys(t, db, entityID))
}

func TestTranslationProjectionBindsLocaleRows(t *testing.T) {
	db := projectionUnitDB(t)
	entityID, assetID := uuid.NewString(), uuid.NewString()
	seedProjectionReadyAsset(t, db, assetID)
	authoredAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, db.Exec("INSERT INTO page_translation (entity_id, locale, updated_at) VALUES (?, 'ko', ?)", entityID, authoredAt).Error)
	projection := NewTranslationProjection(TranslationProjectionSpec{EntityType: "page", Localeless: LocalelessEntityRow})

	err := projection.Complete(t.Context(), db, og.Target{
		EntityType: "page", EntityID: entityID, Locale: new("ko"), Kind: "locale",
	}, assetID, projectionTestCDN)
	require.NoError(t, err)
	var stored *string
	require.NoError(t, db.Table("page_translation").Select("og_asset_id").Where("entity_id = ?", entityID).Scan(&stored).Error)
	require.Equal(t, assetID, *stored)
	require.Equal(t, []string{"og_ko"}, projectionBindingKeys(t, db, entityID))
	var updatedAt time.Time
	require.NoError(t, db.Table("page_translation").Select("updated_at").Where("entity_id = ?", entityID).Scan(&updatedAt).Error)
	require.True(t, authoredAt.Equal(updatedAt), "a derived OG image must not change the translation revision")

	err = projection.Complete(t.Context(), db, og.Target{
		EntityType: "page", EntityID: entityID, Locale: new("en"), Kind: "locale",
	}, assetID, projectionTestCDN)
	require.ErrorIs(t, err, og.ErrTranslationTargetMissing)
	err = projection.Complete(t.Context(), db, og.Target{EntityType: "post", EntityID: entityID}, assetID, projectionTestCDN)
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
}

func TestEntityProjectionOwnsOnlyTheEntityRow(t *testing.T) {
	db := projectionUnitDB(t)
	entityID, assetID := uuid.NewString(), uuid.NewString()
	seedProjectionReadyAsset(t, db, assetID)
	require.NoError(t, db.Exec("INSERT INTO page (id) VALUES (?)", entityID).Error)
	projection := NewEntityProjection("page")

	err := projection.Complete(t.Context(), db, og.Target{
		EntityType: "page", EntityID: entityID, Locale: new("ko"), Kind: "locale",
	}, assetID, projectionTestCDN)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	err = projection.Complete(t.Context(), db, og.Target{EntityType: "page", EntityID: uuid.NewString()}, assetID, projectionTestCDN)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	require.NoError(t, projection.Complete(t.Context(), db, og.Target{EntityType: "page", EntityID: entityID}, assetID, projectionTestCDN))
	require.Equal(t, assetID, projectionPointer(t, db, "page", entityID))
	require.Equal(t, []string{"og"}, projectionBindingKeys(t, db, entityID))
}

func projectionUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE page (id text PRIMARY KEY, og_asset_id text, updated_at datetime);
	CREATE TABLE page_translation (
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

func seedProjectionReadyAsset(t *testing.T, db *gorm.DB, assetID string) {
	t.Helper()
	require.NoError(t, db.Exec(`INSERT INTO public_asset (
		id, kind, object_key, extension, mime_type, file_size, sha256,
		disposition, status, ready_at, created_at, updated_at
	) VALUES (?, 'og', ?, 'webp', 'image/webp', 1, ?, 'inline', 'ready', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		assetID, "asset/"+assetID+".webp", make([]byte, 32),
	).Error)
}

func projectionPointer(t *testing.T, db *gorm.DB, table, entityID string) string {
	t.Helper()
	var stored *string
	require.NoError(t, db.Table(table).Select("og_asset_id").Where("id = ?", entityID).Scan(&stored).Error)
	require.NotNil(t, stored)
	return *stored
}

func projectionBindingKeys(t *testing.T, db *gorm.DB, entityID string) []string {
	t.Helper()
	var keys []string
	require.NoError(t, db.Table("public_asset_binding").Where("owner_id = ?", entityID).Order("binding_key").Pluck("binding_key", &keys).Error)
	return keys
}
