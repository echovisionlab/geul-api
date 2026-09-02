package filemedia

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const maxDownloadFilenameBytes = 255

func normalizeNewDownloadFilename(value string) (string, error) {
	normalized := norm.NFC.String(strings.TrimSpace(value))
	if normalized == "" {
		return "", fmt.Errorf("must not be empty")
	}
	if !validDownloadFilename(normalized) {
		return "", fmt.Errorf("must be valid UTF-8, at most %d bytes, and contain no path separators or control characters", maxDownloadFilenameBytes)
	}
	return normalized, nil
}

func validDownloadFilename(value string) bool {
	if len(value) > maxDownloadFilenameBytes ||
		!utf8.ValidString(value) ||
		value == "." ||
		value == ".." {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

// CanonicalDownloadFilename returns a mediaauth-safe filename for both new and
// historical file rows. Historical invalid values are never echoed into signed
// token claims; they receive a deterministic file-ID fallback instead.
func CanonicalDownloadFilename(
	fileName *string,
	fileID string,
	extension string,
) string {
	extension = strings.ToLower(strings.TrimSpace(extension))
	if fileName != nil {
		normalized := norm.NFC.String(strings.TrimSpace(*fileName))
		if normalized != "" && validDownloadFilename(normalized) {
			if extension == "" || strings.EqualFold(filepathExtension(normalized), extension) {
				return normalized
			}
			return normalized + "." + extension
		}
	}
	if extension != "" {
		return "download-" + strings.TrimSpace(fileID) + "." + extension
	}
	return "download-" + strings.TrimSpace(fileID)
}

func filepathExtension(value string) string {
	index := strings.LastIndexByte(value, '.')
	if index < 0 || index == len(value)-1 {
		return ""
	}
	return value[index+1:]
}

func storedFileBasename(fileName string, fileID string, extension string) string {
	normalized := norm.NFC.String(strings.TrimSpace(fileName))
	extension = strings.ToLower(strings.TrimSpace(extension))
	if extension != "" && strings.EqualFold(filepathExtension(normalized), extension) {
		normalized = strings.TrimSpace(normalized[:len(normalized)-len(extension)-1])
	}
	if normalized != "" && validDownloadFilename(normalized) {
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
	return CanonicalDownloadFilename(
		optionalNonEmptyString(normalized),
		fileID,
		mediaExtension(&mimeType),
	)
}
