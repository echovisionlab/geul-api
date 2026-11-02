package mediaasset

import "slices"

type Disposition int32

const (
	DispositionUnspecified Disposition = iota
	DispositionInline
	DispositionAttachment
)

type WriteTarget struct {
	AssetID          string
	ObjectKey        string
	Extension        string
	MimeType         string
	Disposition      Disposition
	DownloadFilename *string
}
type WrittenAsset struct {
	AssetID  string
	FileSize int64
	SHA256   []byte
}
type WrittenGeneration struct {
	GenerationID   string
	ManifestSHA256 []byte
	ObjectCount    int32
	TotalSize      int64
}
type Reference struct {
	DownloadFilename *string
	AssetID          string
	URL              string
	Extension        string
	MimeType         string
	FileSize         int64
	SHA256           []byte
	Disposition      Disposition
}

// Clone copies a reference and its mutable fields; nil stays nil.
func (ref *Reference) Clone() *Reference {
	if ref == nil {
		return nil
	}
	result := *ref
	result.SHA256 = slices.Clone(ref.SHA256)
	if ref.DownloadFilename != nil {
		filename := *ref.DownloadFilename
		result.DownloadFilename = &filename
	}
	return &result
}
