package formpostgres

import (
	"github.com/echovisionlab/geul-api/internal/form"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// AssetOf maps a public asset the media runtime projects to the domain
// Asset; nil stays nil.
func AssetOf(asset *commonv1.AssetRef) *form.Asset {
	if asset == nil {
		return nil
	}
	return &form.Asset{
		ID: asset.AssetId, URL: asset.Url, Extension: asset.Extension, MIMEType: asset.MimeType, FileSize: asset.FileSize,
		SHA256: asset.Sha256, Disposition: form.AssetDisposition(asset.Disposition), DownloadFilename: asset.DownloadFilename,
	}
}
