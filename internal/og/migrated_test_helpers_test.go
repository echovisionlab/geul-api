package og_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type migratedRenderConfig struct{}

type ogAssetSnapshotForTest struct {
	AssetID   string `json:"asset_id"`
	URL       string `json:"url"`
	Extension string `json:"extension"`
	MimeType  string `json:"mime_type"`
}

type ogOutputSnapshotForTest struct {
	AssetID   string `json:"asset_id"`
	ObjectKey string `json:"object_key"`
	Extension string `json:"extension"`
	MimeType  string `json:"mime_type"`
}

type ogEntitySnapshotForTest struct {
	EntityType    string                  `json:"entity_type"`
	EntityID      string                  `json:"entity_id"`
	Title         string                  `json:"title"`
	Locale        *string                 `json:"locale,omitempty"`
	FeaturedImage *ogAssetSnapshotForTest `json:"featured_image,omitempty"`
	Output        ogOutputSnapshotForTest `json:"output"`
}

func (migratedRenderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

type migratedProjection struct{}

func (migratedProjection) Handles(og.Target) bool { return true }
func (migratedProjection) ReleasePending(context.Context, *gorm.DB, og.Target, string) error {
	return nil
}
func (migratedProjection) Complete(context.Context, *gorm.DB, og.Target, string, time.Time, string) error {
	return nil
}

func newServiceUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE file (
			id text PRIMARY KEY, file_name text, mime_type text, file_size integer,
			extension text, sha256 blob, duration_seconds integer,
			ingest_slot_id text, ingest_attempt_id text,
			delete_requested_at datetime, created_at datetime
		)`,
		`CREATE TABLE public_asset (
			id text PRIMARY KEY, source_file_id text, kind text NOT NULL,
			object_key text NOT NULL UNIQUE, extension text NOT NULL, mime_type text NOT NULL,
			file_size integer, sha256 blob, disposition text NOT NULL, download_filename text,
			status text NOT NULL, ready_at datetime, delete_requested_at datetime, deleted_at datetime,
			failed_at datetime, failure_reason text, created_at datetime NOT NULL, updated_at datetime NOT NULL
		)`,
		`CREATE TABLE public_asset_binding (
			asset_id text NOT NULL, owner_type text NOT NULL, owner_id text NOT NULL,
			binding_key text NOT NULL, source_file_id text, created_at datetime NOT NULL,
			updated_at datetime NOT NULL, PRIMARY KEY (owner_type, owner_id, binding_key)
		)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	return db
}

func setupOgLifecycleUnitTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE site_settings (
			id integer PRIMARY KEY, site_title text NOT NULL DEFAULT '',
			primary_color text NOT NULL DEFAULT '#b02d23', logo_light_file_id text,
			site_og_asset_id text, og_image_config blob,
			created_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE og_generation_run (
			id text PRIMARY KEY, trigger_kind text, reason text, render_config_snapshot blob,
			config_revision text, status text, started_at datetime, completed_at datetime,
			created_at datetime, updated_at datetime
		)`,
		`CREATE TABLE og_generation_target (
			id text PRIMARY KEY, entity_type text, entity_id text, target_kind text, locale text,
			latest_generation_id text, created_at datetime, updated_at datetime,
			UNIQUE(entity_type, entity_id, locale)
		)`,
		`CREATE TABLE og_generation (
			id text PRIMARY KEY, run_id text, target_id text, request_sequence integer DEFAULT 0,
			status text, entity_snapshot blob, processing_at datetime, lease_token text,
			lease_expires_at datetime, deadline_at datetime, last_error_code text,
			ready_at datetime, failed_at datetime, superseded_at datetime,
			superseded_by_id text, cancelled_at datetime, completed_at datetime,
			created_at datetime, updated_at datetime
		)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	for _, table := range []string{"post_translation", "page_translation", "series_translation", "form_translation"} {
		require.NoError(t, db.Exec("CREATE TABLE "+table+" (entity_id text, locale text, og_asset_id text)").Error)
	}
	for _, table := range []string{"work"} {
		require.NoError(t, db.Exec("CREATE TABLE "+table+" (id text PRIMARY KEY, title text, featured_image_file_id text, og_asset_id text, updated_at datetime)").Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO site_settings (id) VALUES (1)`).Error)
}

func newOGPlannerForTest(db *gorm.DB, cdnDomain string) *og.Planner {
	return og.NewPlanner(db, cdnDomain, migratedRenderConfig{}, migratedProjection{})
}

func newOGLifecycleForTest(db *gorm.DB, cdnDomain string) *og.Lifecycle {
	return og.NewLifecycle(db, cdnDomain, migratedProjection{})
}

func ogTestRequest(entityType managev1.OgEntityType, entityID, title string, locale, featuredImageFileID *string) og.Request {
	policy, _ := og.PolicyForEntityType(entityType)
	kind := "entity"
	if locale != nil {
		kind = "locale"
	}
	target := og.Target{EntityType: policy.Name, EntityID: entityID, Locale: locale, Kind: kind}
	return og.Request{Target: target, Title: title, FeaturedImageFileID: featuredImageFileID}
}

func requestOgGenerationForTest(ctx context.Context, planner *og.Planner, triggerKind, reason string, requests []og.Request) (*og.Plan, error) {
	return planner.RequestBulk(ctx, triggerKind, reason, requests, func(context.Context, *gorm.DB) ([]og.Request, error) {
		return requests, nil
	})
}

func cancelOgGenerationEntityForTest(ctx context.Context, lifecycle *og.Lifecycle, entityType managev1.OgEntityType, entityID string) error {
	return lifecycle.CancelEntity(ctx, entityType, entityID)
}

func stringPtr(value string) *string { return &value }
