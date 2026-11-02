package filemedia_test

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestFileDeliveryReadsUseReadySourceAssets(t *testing.T) {
	for _, kind := range []string{"image", "mesh"} {
		t.Run(kind, func(t *testing.T) {
			f := newLibraryFixture(t)
			row := filemediatest.CatalogFile("original", nil)
			extension, mime := "webp", "image/webp"
			if kind == "mesh" {
				extension, mime = "glb", "model/gltf-binary"
			}
			row.Extension = &extension
			row.MimeType = &mime
			f.store.Files[row.ID] = filemediatest.CatalogFileRecord{Row: row}
			assetID := uuid.NewString()
			now := time.Now().UTC()
			size := int64(64)
			f.store.MediaAssets.PutAsset(mediaasset.Asset{ID: assetID, SourceFileID: &row.ID, Kind: kind, ObjectKey: "asset/" + assetID + "." + extension, Extension: extension, MimeType: mime, FileSize: &size, SHA256: make([]byte, 32), Disposition: "inline", Status: mediaasset.AssetReady, CreatedAt: now, UpdatedAt: now})
			service := filemedia.ExportDeliveryService(f.units)
			single, err := filemedia.ExportGetFileURLs(service, f.ctx, row.ID)
			require.NoError(t, err)
			bulk, err := filemedia.ExportLoadFileURLs(service, f.ctx, []string{row.ID})
			require.NoError(t, err)
			require.Equal(t, assetID, single.Delivery.Asset.AssetID)
			require.Equal(t, single.Delivery.Asset, bulk[row.ID].Delivery.Asset)
			require.Equal(t, "https://cdn.example.com/asset/"+assetID+"/"+kind+"."+extension, single.Delivery.Asset.URL)
			require.True(t, strings.HasSuffix(single.Delivery.Inline.URL, "/"+row.ID+"."+extension))
			require.Equal(t, filemedia.ProcessingReady, single.Delivery.ProcessingStatus)
		})
	}
}
func TestFileDeliveryReadsPreserveMissingAndDeletingErrors(t *testing.T) {
	f := newLibraryFixture(t)
	service := filemedia.ExportDeliveryService(f.units)
	for _, pending := range []bool{false, true} {
		row := filemediatest.CatalogFile("missing", nil)
		if pending {
			f.store.Files[row.ID] = filemediatest.CatalogFileRecord{Row: row, PendingDeletion: true}
		}
		got, err := filemedia.ExportGetFileURLs(service, f.ctx, row.ID)
		require.Nil(t, got)
		require.Equal(t, connect.CodeNotFound, errs.CodeOf(err))
		require.EqualError(t, errs.Wrap(err), errs.NotFound("file", row.ID).Error())
	}
}
