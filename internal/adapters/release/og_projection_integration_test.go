//go:build integration

package release

import (
	"testing"
	"time"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestProjectionPreservesLegacyReleaseBindingIntegration(t *testing.T) {
	pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true})
	db := pg.DB
	now := time.Now().UTC()
	releaseID, documentID, assetID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO content_document (id, profile, created_at, updated_at)
		VALUES (?::uuid, 'compact', ?, ?)`,
		documentID, now, now,
	).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO release (id, type, status, content_document_id, created_at, updated_at)
		VALUES (?::uuid, ?::release_type, ?, ?::uuid, ?, ?)`,
		releaseID,
		managev1.ReleaseType_RELEASE_TYPE_ALBUM.String(),
		managev1.ReleaseStatus_RELEASE_STATUS_DRAFT.String(),
		documentID,
		now,
		now,
	).Error)
	fileSize := int64(123)
	digest := make([]byte, 32)
	objectKey, err := mediaauth.AssetObjectKey(assetID, "webp")
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, Kind: "og", ObjectKey: objectKey,
		Extension: "webp", MimeType: "image/webp", FileSize: &fileSize,
		SHA256: digest, Disposition: "inline", Status: model.PublicAssetStatusReady,
		ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, NewProjection().Complete(t.Context(), db, og.Target{
		EntityType: "release", EntityID: releaseID, Kind: "entity",
	}, assetID, now, "https://cdn.example.com"))

	var releaseAssetID *string
	require.NoError(t, db.Table("release").Select("og_asset_id").Where("id = ?", releaseID).Scan(&releaseAssetID).Error)
	require.NotNil(t, releaseAssetID)
	require.Equal(t, assetID, *releaseAssetID)
	var binding model.PublicAssetBinding
	require.NoError(t, db.First(&binding,
		"owner_type = ? AND owner_id = ? AND binding_key = ?", "release", releaseID, "og",
	).Error)
	require.Equal(t, assetID, binding.AssetID)
}
