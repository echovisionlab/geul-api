//go:build integration

package faviconpostgres_test

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/favicon/favicontest"
	faviconpostgres "github.com/echovisionlab/geul-api/internal/favicon/postgres"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPostgresStoreContractIntegration(t *testing.T) {
	t.Parallel()
	favicontest.RunContract(t, func(t *testing.T) favicontest.Harness {
		db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		return favicontest.Harness{
			Harness: mutationtest.PostgresHarness(db, faviconpostgres.NewUnits(db, "https://cdn.example.com")),
			SeedFile: func(t *testing.T, id, mime string) {
				require.NoError(t, db.Create(&model.File{ID: id, FileName: "favicon", Extension: "png", MimeType: mime, FileSize: 64}).Error)
			},
			SeedAsset: func(t *testing.T, file, typ string, a favicon.DerivativeAsset) {
				row := mediaassetpostgres.AssetRow(mediaasset.Asset{ID: a.ID, SourceFileID: a.SourceFileID, Kind: a.Kind, ObjectKey: a.ObjectKey, Extension: a.Extension, MimeType: a.MimeType, FileSize: a.FileSize, SHA256: a.SHA256, Disposition: a.Disposition, DownloadFilename: a.DownloadFilename, Status: mediaasset.AssetStatus(a.Status), CreatedAt: a.CreatedAt, UpdatedAt: a.CreatedAt})
				if a.Status == favicon.AssetFailed {
					failed := a.CreatedAt
					row.FailedAt = &failed
				}
				if a.Status == favicon.AssetDeleted {
					deleted := a.CreatedAt
					row.DeletedAt = &deleted
				}
				if a.Status == favicon.AssetReady {
					ready := a.CreatedAt
					row.ReadyAt = &ready
				}
				require.NoError(t, db.Create(&row).Error)
				if typ != "" {
					require.NoError(t, db.Table("file_derivative").Create(map[string]any{"id": uuid.NewString(), "file_id": file, "type": typ, "asset_id": a.ID, "created_at": time.Now().UTC()}).Error)
				}
			},
			SeedBinding: func(t *testing.T, id string) {
				require.NoError(t, db.Create(&model.PublicAssetBinding{AssetID: id, OwnerType: "post", OwnerID: uuid.NewString(), BindingKey: "featured_image"}).Error)
			},
		}
	})
}
