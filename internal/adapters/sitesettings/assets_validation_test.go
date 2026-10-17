package sitesettings

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAssetsValidateAttachment(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE file ( client_media_bundle_id TEXT,id TEXT PRIMARY KEY, mime_type TEXT NOT NULL, file_size INTEGER NOT NULL)`).Error)
	insert := func(mimeType string, fileSize int64) string {
		fileID := uuid.NewString()
		require.NoError(t, db.Exec(`INSERT INTO file (id, mime_type, file_size) VALUES (?, ?, ?)`, fileID, mimeType, fileSize).Error)
		return fileID
	}
	assets := NewAssets("")
	for _, key := range []string{"logo_full_light_file_id", "logo_full_dark_file_id", "logo_symbol_light_file_id", "logo_symbol_dark_file_id"} {
		require.NoError(t, assets.ValidateAttachment(context.Background(), db, key, insert("image/png", 1024)), key)
		require.NoError(t, assets.ValidateAttachment(context.Background(), db, key, insert("image/svg+xml", 1024)), key)
		require.Error(t, assets.ValidateAttachment(context.Background(), db, key, insert("image/webp", 1024)), key)
	}
	for _, key := range []string{"logo_email_light_file_id", "logo_email_dark_file_id"} {
		require.NoError(t, assets.ValidateAttachment(context.Background(), db, key, insert("image/png", 1024)), key)
		require.Error(t, assets.ValidateAttachment(context.Background(), db, key, insert("image/svg+xml", 1024)), key)
	}
	require.Error(t, assets.ValidateAttachment(context.Background(), db, "loader", insert("image/webp", 100*1024+1)))
}
