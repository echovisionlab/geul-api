package sitesettings

import (
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	domain "github.com/echovisionlab/geul-api/internal/sitesettings"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBrandingReusesReadyLibraryImageAcrossBindingReadsAndOG(t *testing.T) {
	for _, tc := range []struct{ kind, extension, mime, slot, expected string }{
		{"image", "svg", "image/svg+xml", "logo:light", "logo"},
		{"image", "png", "image/png", "logo:light", "logo"},
		{"image", "png", "image/png", "logo:email", "email_image"},
		{"logo", "svg", "image/svg+xml", "logo:light", "logo"},
	} {
		t.Run(tc.kind+"/"+tc.extension+"/"+tc.slot, func(t *testing.T) {
			db := brandingAssetDB(t)
			fileID, assetID := seedBrandingAsset(t, db, tc.kind, tc.extension, tc.mime, model.PublicAssetStatusReady)
			assets := NewAssets("https://cdn.example.com")
			ref, err := assets.BindReady(t.Context(), db, domain.AssetBinding{SourceFileID: fileID, Key: tc.slot, Kind: tc.expected})
			require.NoError(t, err)
			require.Equal(t, assetID, ref.GetAssetId())
			read, err := assets.ReadyRef(t.Context(), db, fileID, tc.expected)
			require.NoError(t, err)
			require.Equal(t, ref, read)
			// Retrying binds the same immutable asset, and retains the original source identity.
			_, err = assets.BindReady(t.Context(), db, domain.AssetBinding{SourceFileID: fileID, Key: tc.slot, Kind: tc.expected})
			require.NoError(t, err)
			var binding model.PublicAssetBinding
			require.NoError(t, db.Where("binding_key = ?", tc.slot).Take(&binding).Error)
			require.Equal(t, fileID, *binding.SourceFileID)
			require.Equal(t, assetID, binding.AssetID)
			var stored model.PublicAsset
			require.NoError(t, db.Take(&stored).Error)
			require.Equal(t, tc.kind, stored.Kind)
			var count int64
			require.NoError(t, db.Model(&model.PublicAsset{}).Count(&count).Error)
			require.EqualValues(t, 1, count)
			if tc.expected == "logo" {
				require.NoError(t, db.Exec("UPDATE site_settings SET logo_light_file_id = ? WHERE id = 1", fileID).Error)
				payload, _, err := NewRenderConfig().Snapshot(t.Context(), db, "https://cdn.example.com")
				require.NoError(t, err)
				var snapshot renderSnapshot
				require.NoError(t, json.Unmarshal(payload, &snapshot))
				require.NotNil(t, snapshot.LogoAsset)
				require.Equal(t, assetID, snapshot.LogoAsset.AssetID)
			}
		})
	}
}

func TestBrandingRejectsIncompatibleOrUnreadyLibraryAssets(t *testing.T) {
	for _, tc := range []struct{ kind, extension, mime, state, expected string }{
		{"image", "webp", "image/webp", model.PublicAssetStatusReady, "logo"},
		{"image", "svg", "image/svg+xml", model.PublicAssetStatusReady, "email_image"},
		{"image", "svg", "image/svg+xml", model.PublicAssetStatusAllocated, "logo"},
		{"image", "svg", "image/svg+xml", model.PublicAssetStatusDeletePending, "logo"},
		{"image", "svg", "image/svg+xml", model.PublicAssetStatusReady, "favicon"},
		{"poster", "svg", "image/svg+xml", model.PublicAssetStatusReady, "logo"},
	} {
		t.Run(tc.kind+"/"+tc.extension+"/"+tc.state+"/"+tc.expected, func(t *testing.T) {
			db := brandingAssetDB(t)
			fileID, _ := seedBrandingAsset(t, db, tc.kind, tc.extension, tc.mime, tc.state)
			_, err := NewAssets("https://cdn.example.com").BindReady(t.Context(), db, domain.AssetBinding{SourceFileID: fileID, Key: "logo:light", Kind: tc.expected})
			require.Error(t, err)
			var count int64
			require.NoError(t, db.Model(&model.PublicAssetBinding{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestBrandingLibraryImageCannotAttachAfterSourceDeletionIntent(t *testing.T) {
	db := brandingAssetDB(t)
	fileID, _ := seedBrandingAsset(t, db, "image", "svg", "image/svg+xml", model.PublicAssetStatusReady)
	require.NoError(t, db.Exec("UPDATE file SET delete_requested_at = ? WHERE id = ?", time.Now().UTC(), fileID).Error)
	_, err := NewAssets("https://cdn.example.com").BindReady(t.Context(), db, domain.AssetBinding{SourceFileID: fileID, Key: "logo:light", Kind: "logo"})
	require.ErrorContains(t, err, "file is pending deletion")
}

func brandingAssetDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := siteOGUnitDB(t)
	for _, sql := range []string{
		`CREATE TABLE file (id text PRIMARY KEY, delete_requested_at datetime)`,
		`CREATE TABLE public_asset (id text PRIMARY KEY, source_file_id text, kind text, object_key text,
   extension text, mime_type text, file_size integer, sha256 blob, disposition text, download_filename text,
   status text, ready_at datetime, delete_requested_at datetime, deleted_at datetime, failed_at datetime,
   failure_reason text, created_at datetime, updated_at datetime)`,
		`CREATE TABLE public_asset_binding (asset_id text, owner_type text, owner_id text, binding_key text,
   source_file_id text, created_at datetime, updated_at datetime, PRIMARY KEY(owner_type,owner_id,binding_key))`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	return db
}

func seedBrandingAsset(t *testing.T, db *gorm.DB, kind, extension, mime, state string) (string, string) {
	t.Helper()
	fileID, assetID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec("INSERT INTO file (id) VALUES (?)", fileID).Error)
	size := int64(1024)
	digest := sha256.Sum256([]byte("verified SVG or PNG bytes"))
	require.NoError(t, db.Create(&model.PublicAsset{ID: assetID, SourceFileID: &fileID, Kind: kind,
		ObjectKey: "asset/" + assetID + "." + extension, Extension: extension, MimeType: mime, FileSize: &size,
		SHA256: digest[:], Disposition: "inline", Status: state, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}).Error)
	return fileID, assetID
}
