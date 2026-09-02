package filemedia

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// createFileAttachmentReferenceTablesForServiceTests mirrors the canonical
// product-reference registry with only the columns needed by deletion
// preflight. It intentionally excludes retired per-entity attachment
// projection tables.
func createFileAttachmentReferenceTablesForServiceTests(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS artist_file (artist_id TEXT, file_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS release_file (release_id TEXT, file_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS content_block (
			id TEXT PRIMARY KEY,
			document_id TEXT NOT NULL,
			kind TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS content_block_attachment (block_id TEXT, reference_path TEXT, selector_kind TEXT, file_id TEXT, missing_kind TEXT)`,
		`CREATE TABLE IF NOT EXISTS program_event_media (event_id TEXT, file_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS track (id TEXT, audio_original_file_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS client (id TEXT, logo_light_file_id TEXT, logo_dark_file_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS label (id TEXT, logo_light_file_id TEXT, logo_dark_file_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS series (id TEXT, featured_image_file_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS post (id TEXT PRIMARY KEY, featured_image_file_id TEXT, content_document_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS page (id TEXT PRIMARY KEY, featured_image_file_id TEXT, content_document_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS work (id TEXT PRIMARY KEY, featured_image_file_id TEXT, content_document_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS form (id TEXT, featured_image_file_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS map_place (id TEXT, image_file_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS program_event_series (id TEXT, poster_file_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS site_settings (
			id TEXT, logo_light_file_id TEXT, logo_dark_file_id TEXT,
			logo_email_file_id TEXT, favicon_file_id TEXT,
			site_og_background_file_id TEXT, privacy_og_background_file_id TEXT,
			terms_og_background_file_id TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS site_setting_loader_file (site_setting_id TEXT, file_id TEXT)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
}
