// Package memberref contains the Member references shared by domain ports.
package memberref

import "github.com/echovisionlab/geul-api/internal/mediaasset"

// AssetDisposition is the content disposition of a public asset, numbered as
// the API's AssetDisposition.
type AssetDisposition int32

// Asset is a ready public asset as the media runtime projects it.
type Asset struct {
	ID               string
	URL              string
	Extension        string
	MIMEType         string
	FileSize         int64
	SHA256           []byte
	Disposition      AssetDisposition
	DownloadFilename *string
}

// Summary is the Member reference shown by other domains.
type Summary struct {
	ID       string
	Nickname string
	Avatar   *Asset
	Deleted  bool
}

// Reference projects an optional avatar as a public asset reference.
func (asset *Asset) Reference() *mediaasset.Reference {
	if asset == nil {
		return nil
	}
	return &mediaasset.Reference{AssetID: asset.ID, URL: asset.URL, Extension: asset.Extension, MimeType: asset.MIMEType, FileSize: asset.FileSize, SHA256: append([]byte(nil), asset.SHA256...), Disposition: mediaasset.Disposition(asset.Disposition), DownloadFilename: asset.DownloadFilename}
}

// AssetFromReference reads an optional avatar from a public asset reference.
func AssetFromReference(ref *mediaasset.Reference) *Asset {
	if ref == nil {
		return nil
	}
	return &Asset{ID: ref.AssetID, URL: ref.URL, Extension: ref.Extension, MIMEType: ref.MimeType, FileSize: ref.FileSize, SHA256: append([]byte(nil), ref.SHA256...), Disposition: AssetDisposition(ref.Disposition), DownloadFilename: ref.DownloadFilename}
}
