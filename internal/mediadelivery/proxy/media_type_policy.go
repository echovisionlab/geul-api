package proxy

import (
	"mime"
	"net/http"
	"path"
	"strings"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
)

func deliveryExtension(deliveryPath mediaauth.DeliveryPath) string {
	if deliveryPath.Extension != "" {
		return deliveryPath.Extension
	}
	return strings.TrimPrefix(strings.ToLower(path.Ext(deliveryPath.ObjectName)), ".")
}

func pathKindAllowsContentType(kind mediaauth.PathKind, extension, contentType string) bool {
	allowedTypes, ok := mediaTypesByPathKind(kind)
	if !ok {
		return false
	}
	extension = strings.ToLower(strings.TrimSpace(extension))
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || extension == "" {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	for _, allowed := range allowedTypes[extension] {
		if mediaType == allowed {
			return true
		}
	}
	return false
}

func mediaTypesByPathKind(kind mediaauth.PathKind) (map[string][]string, bool) {
	switch kind {
	case mediaauth.PathAsset:
		return publicAssetMediaTypesByExtension, true
	case mediaauth.PathMediaObject:
		return signedMediaObjectTypesByExtension, true
	case mediaauth.PathMediaHLS:
		return publicHLSMediaTypesByExtension, true
	default:
		return nil, false
	}
}

// Public assets are immutable presentation artifacts. Downloadable source
// files use the signed media namespace instead.
var publicAssetMediaTypesByExtension = map[string][]string{
	"avif": {"image/avif"},
	"gif":  {"image/gif"},
	"glb":  {"model/gltf-binary"},
	"ico":  {"image/x-icon", "image/vnd.microsoft.icon"},
	"jpg":  {"image/jpeg"},
	"json": {"application/json"},
	"png":  {"image/png"},
	"svg":  {"image/svg+xml"},
	"webp": {"image/webp"},
}

// Signed media types mirror the API's canonical MIME-to-extension mapping.
var signedMediaObjectTypesByExtension = map[string][]string{
	"7z":   {"application/x-7z-compressed"},
	"aac":  {"audio/aac"},
	"aiff": {"audio/aiff", "audio/x-aiff"},
	"avi":  {"video/avi", "video/x-msvideo"},
	"avif": {"image/avif"},
	"csv":  {"text/csv"},
	"doc":  {"application/msword"},
	"docx": {"application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
	"flac": {"audio/flac"},
	"gif":  {"image/gif"},
	"glb":  {"model/gltf-binary"},
	"ico":  {"image/x-icon", "image/vnd.microsoft.icon"},
	"jpg":  {"image/jpeg"},
	"json": {"application/json"},
	"m4a":  {"audio/mp4", "audio/m4a", "audio/x-m4a", "audio/mp4a-latm"},
	"mkv":  {"video/matroska", "video/mkv", "video/x-matroska", "application/x-matroska"},
	"mov":  {"video/quicktime"},
	"mp3":  {"audio/mpeg"},
	"mp4":  {"video/mp4"},
	"ogg":  {"audio/ogg"},
	"pdf":  {"application/pdf"},
	"png":  {"image/png"},
	"ppt":  {"application/vnd.ms-powerpoint"},
	"pptx": {"application/vnd.openxmlformats-officedocument.presentationml.presentation"},
	"rar":  {"application/x-rar-compressed"},
	"svg":  {"image/svg+xml"},
	"txt":  {"text/plain"},
	"wav":  {"audio/wav"},
	"weba": {"audio/webm"},
	"webm": {"video/webm"},
	"webp": {"image/webp"},
	"xls":  {"application/vnd.ms-excel"},
	"xlsx": {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
	"zip":  {"application/zip", "application/x-zip-compressed"},
}

var publicHLSMediaTypesByExtension = map[string][]string{
	"m3u8": {"application/vnd.apple.mpegurl", "application/x-mpegurl"},
	"m4s":  {"video/iso.segment"},
	"ts":   {"video/mp2t"},
}

func isImageContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && strings.HasPrefix(strings.ToLower(mediaType), "image/")
}

var imageTransformQueryKeys = []string{"fit", "format", "h", "q", "w"}

func hasImageTransformQuery(r *http.Request) bool {
	query := r.URL.Query()
	for _, key := range imageTransformQueryKeys {
		if _, ok := query[key]; ok {
			return true
		}
	}
	return false
}
