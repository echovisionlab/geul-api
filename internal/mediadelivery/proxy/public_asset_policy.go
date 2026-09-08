package proxy

import (
	"mime"
	"strings"

	"github.com/minio/minio-go/v7"
)

func hasForbiddenPublicAssetDownloadMetadata(stat minio.ObjectInfo) bool {
	return hasForbiddenResponseMetadata(stat.Metadata) || hasForbiddenUserMetadata(stat.UserMetadata)
}

func hasForbiddenResponseMetadata(metadata map[string][]string) bool {
	for key, values := range metadata {
		if isPublicAssetDispositionMetadataKey(key) && containsUnsafeDisposition(values) {
			return true
		}
		if isPublicAssetDownloadFilenameMetadataKey(key) && containsNonemptyMetadataValue(values) {
			return true
		}
	}
	return false
}

func hasForbiddenUserMetadata(metadata map[string]string) bool {
	for key, value := range metadata {
		if (strings.EqualFold(key, "Disposition") || isPublicAssetDispositionMetadataKey(key)) &&
			!isSafePublicAssetDisposition(value) {
			return true
		}
		if (strings.EqualFold(key, "Download-Filename") || isPublicAssetDownloadFilenameMetadataKey(key)) &&
			strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func containsUnsafeDisposition(values []string) bool {
	for _, value := range values {
		if !isSafePublicAssetDisposition(value) {
			return true
		}
	}
	return false
}

func isPublicAssetDispositionMetadataKey(key string) bool {
	return equalFoldAny(key, "Content-Disposition", "X-Amz-Meta-Disposition", "X-Minio-Meta-Disposition")
}

func isPublicAssetDownloadFilenameMetadataKey(key string) bool {
	return equalFoldAny(key, "Download-Filename", "X-Amz-Meta-Download-Filename", "X-Minio-Meta-Download-Filename")
}

func equalFoldAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(value, candidate) {
			return true
		}
	}
	return false
}

func containsNonemptyMetadataValue(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func isSafePublicAssetDisposition(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	disposition, parameters, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(disposition, "inline") && len(parameters) == 0
}
