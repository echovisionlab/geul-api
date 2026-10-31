package filemedia

import (
	"fmt"
	"strings"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"golang.org/x/text/unicode/norm"
)

const maxDownloadFilenameBytes = 255

func normalizeNewDownloadFilename(value string) (string, error) {
	normalized := norm.NFC.String(strings.TrimSpace(value))
	if normalized == "" {
		return "", fmt.Errorf("must not be empty")
	}
	if !mediaauth.ValidDownloadFilename(normalized) {
		return "", fmt.Errorf("must be valid UTF-8, at most %d bytes, and contain no path separators or control characters", maxDownloadFilenameBytes)
	}
	return normalized, nil
}

func storedFileBasename(fileName string, fileID string, extension string) string {
	normalized := norm.NFC.String(strings.TrimSpace(fileName))
	extension = strings.ToLower(strings.TrimSpace(extension))
	if extension != "" && strings.EqualFold(mediaauth.FilenameExtension(normalized), extension) {
		normalized = strings.TrimSpace(normalized[:len(normalized)-len(extension)-1])
	}
	if normalized != "" && mediaauth.ValidDownloadFilename(normalized) {
		return normalized
	}
	return strings.TrimSpace(fileID)
}

func canonicalRemoteImportFilename(
	fileName string,
	fileID string,
	mimeType string,
) string {
	normalized := normalizeRemoteImportStoredFileName(fileName, mimeType)
	return mediaauth.CanonicalDownloadFilename(
		optionalNonEmptyString(normalized),
		fileID,
		mediaExtension(&mimeType),
	)
}

func mediaExtension(mimeType *string) string {
	if mimeType == nil || *mimeType == "" {
		return "bin"
	}
	return model.GetExtensionFromMime(*mimeType)
}
