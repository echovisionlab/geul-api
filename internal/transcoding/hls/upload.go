package hls

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// Storage uploads generated HLS objects.
type Storage interface {
	Upload(ctx context.Context, key string, localPath string, contentType string) error
	UploadCompleted(ctx context.Context, key string, localPath string, contentType string, completion []byte) error
}

// Upload writes a validated HLS package and returns its result metadata.
func Upload(
	ctx context.Context,
	storage Storage,
	target *commonv1.MediaGenerationWriteTarget,
	pkg *Package,
	completion []byte,
	onProgress func(percent int),
) (*commonv1.MediaGenerationWriteResult, error) {
	prefix := target.GetObjectPrefix()
	files := append(append([]hlsFile(nil), pkg.files...), pkg.manifest)
	for index, file := range files {
		key := prefix + "/" + file.name
		var err error
		if file.name == MasterManifestName {
			err = storage.UploadCompleted(ctx, key, file.path, file.contentType, completion)
		} else {
			err = storage.Upload(ctx, key, file.path, file.contentType)
		}
		if err != nil {
			return nil, fmt.Errorf("upload HLS object %s: %w", file.name, err)
		}
		if onProgress != nil {
			onProgress(((index + 1) * 100) / len(files))
		}
	}
	return pkg.Result(target), nil
}

// Result builds the event-contract result for this package.
func (pkg *Package) Result(target *commonv1.MediaGenerationWriteTarget) *commonv1.MediaGenerationWriteResult {
	return &commonv1.MediaGenerationWriteResult{
		GenerationId:   target.GetGenerationId(),
		ManifestSha256: append([]byte(nil), pkg.manifestHash...),
		ObjectCount:    int32(len(pkg.files) + 1),
		TotalSize:      pkg.totalSize,
	}
}

func hlsContentType(name string) (string, error) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".m3u8":
		return "application/vnd.apple.mpegurl", nil
	case ".ts":
		return "video/mp2t", nil
	default:
		return "", fmt.Errorf("unsupported HLS object %q", name)
	}
}
