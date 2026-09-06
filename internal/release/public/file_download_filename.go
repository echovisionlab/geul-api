package public

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const maxDownloadFilenameBytes = 255

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
