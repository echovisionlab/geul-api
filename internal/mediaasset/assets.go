package mediaasset

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
	AssetID     string
	URL         string
	Extension   string
	MimeType    string
	FileSize    int64
	SHA256      []byte
	Disposition Disposition
}
