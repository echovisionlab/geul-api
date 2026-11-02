package formpostgres

import (
	"github.com/echovisionlab/geul-api/internal/form"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
)

// AssetOf maps a public asset the media runtime projects to the domain
// Asset; nil stays nil.
func AssetOf(asset *mediaasset.Reference) *form.Asset {
	if asset == nil {
		return nil
	}
	return &form.Asset{
		ID: asset.AssetID, URL: asset.URL, Extension: asset.Extension, MIMEType: asset.MimeType, FileSize: asset.FileSize,
		SHA256: asset.SHA256, Disposition: form.AssetDisposition(asset.Disposition), DownloadFilename: asset.DownloadFilename,
	}
}
