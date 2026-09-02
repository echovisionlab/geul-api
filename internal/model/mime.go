package model

import "strings"

// MimeToExtension maps MIME types to file extensions.
var MimeToExtension = map[string]string{
	// Images
	"image/jpeg":               "jpg",
	"image/png":                "png",
	"image/gif":                "gif",
	"image/webp":               "webp",
	"image/avif":               "avif",
	"image/svg+xml":            "svg",
	"image/x-icon":             "ico",
	"image/vnd.microsoft.icon": "ico",
	// Videos
	"video/mp4":              "mp4",
	"video/webm":             "webm",
	"video/quicktime":        "mov",
	"video/avi":              "avi",
	"video/x-msvideo":        "avi",
	"video/matroska":         "mkv",
	"video/mkv":              "mkv",
	"video/x-matroska":       "mkv",
	"application/x-matroska": "mkv",
	// Audio
	"audio/mpeg":      "mp3",
	"audio/wav":       "wav",
	"audio/ogg":       "ogg",
	"audio/webm":      "weba",
	"audio/flac":      "flac",
	"audio/aac":       "aac",
	"audio/mp4":       "m4a",
	"audio/m4a":       "m4a",
	"audio/x-m4a":     "m4a",
	"audio/mp4a-latm": "m4a",
	"audio/aiff":      "aiff",
	"audio/x-aiff":    "aiff",
	// Documents
	"application/pdf":    "pdf",
	"text/plain":         "txt",
	"text/csv":           "csv",
	"application/json":   "json",
	"application/msword": "doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": "docx",
	"application/vnd.ms-excel": "xls",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
	"application/vnd.ms-powerpoint":                                             "ppt",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
	// Archives
	"application/zip":              "zip",
	"application/x-zip-compressed": "zip",
	"application/x-rar-compressed": "rar",
	"application/x-7z-compressed":  "7z",
	// 3D assets
	"model/gltf-binary": "glb",
}

// GetExtensionFromMime returns a file extension for a MIME type.
// Defaults to "bin" when unknown.
func GetExtensionFromMime(mimeType string) string {
	if mimeType == "" {
		return "bin"
	}

	baseType := strings.TrimSpace(strings.Split(mimeType, ";")[0])
	if ext, ok := MimeToExtension[baseType]; ok && ext != "" {
		return ext
	}
	return "bin"
}
