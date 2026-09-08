// Package media validates canonical media target contracts.
package media

import (
	"fmt"
	"strings"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
)

var sourceExtensionByMIME = map[string]string{
	"audio/aac":        "aac",
	"audio/aiff":       "aiff",
	"audio/flac":       "flac",
	"audio/m4a":        "m4a",
	"audio/mp4":        "m4a",
	"audio/mp4a-latm":  "m4a",
	"audio/mpeg":       "mp3",
	"audio/ogg":        "ogg",
	"audio/wav":        "wav",
	"audio/webm":       "weba",
	"audio/x-aiff":     "aiff",
	"audio/x-m4a":      "m4a",
	"video/avi":        "avi",
	"video/matroska":   "mkv",
	"video/mkv":        "mkv",
	"video/mp4":        "mp4",
	"video/quicktime":  "mov",
	"video/webm":       "webm",
	"video/x-matroska": "mkv",
	"video/x-msvideo":  "avi",
}

// ValidateIdentity checks canonical IDs and the supported entity type.
func ValidateIdentity(eventID string, entityType apiv1.TranscodeEntityType, entityID, fileID string) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "event_id", value: eventID},
		{name: "entity_id", value: entityID},
		{name: "file_id", value: fileID},
	} {
		parsed, err := uuid.Parse(field.value)
		if err != nil || parsed.String() != field.value {
			return fmt.Errorf("%s must be a canonical UUID", field.name)
		}
	}

	switch entityType {
	case apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK,
		apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
		apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE:
		return nil
	default:
		return fmt.Errorf("unsupported entity_type %s", entityType)
	}
}

// ValidateSource checks a source target's identity, MIME type, extension, and key.
func ValidateSource(target *commonv1.MediaObjectTarget, expectedFileID, mediaKind string) error {
	if target == nil {
		return fmt.Errorf("target is required")
	}
	if target.GetFileId() != expectedFileID {
		return fmt.Errorf("file_id %q does not match job file_id", target.GetFileId())
	}
	expectedKey, err := canonicalObjectKey(target)
	if err != nil {
		return err
	}
	if err := validateCanonicalMIME(target.GetMimeType(), mediaKind+"/"); err != nil {
		return err
	}
	expectedExtension, known := sourceExtensionByMIME[target.GetMimeType()]
	if !known || target.GetExtension() != expectedExtension {
		return fmt.Errorf("extension does not match verified mime_type")
	}
	if target.GetObjectKey() != expectedKey {
		return fmt.Errorf("object_key must be %q", expectedKey)
	}
	return nil
}

// ValidateHLSOutput checks an HLS generation target's identity and prefix.
func ValidateHLSOutput(target *commonv1.MediaGenerationWriteTarget, expectedFileID string) error {
	if target == nil {
		return fmt.Errorf("target is required")
	}
	if target.GetFileId() != expectedFileID {
		return fmt.Errorf("file_id %q does not match job file_id", target.GetFileId())
	}
	expectedPrefix, err := mediaauth.MediaHLSObjectPrefix(target.GetFileId(), target.GetGenerationId())
	if err != nil {
		return err
	}
	if target.GetObjectPrefix() != expectedPrefix {
		return fmt.Errorf("object_prefix must be %q", expectedPrefix)
	}
	return nil
}

// ValidateAssetOutput checks an asset target's identity, format, and disposition.
func ValidateAssetOutput(
	target *commonv1.AssetWriteTarget,
	extension string,
	mimeType string,
) error {
	if target == nil {
		return fmt.Errorf("target is required")
	}
	if target.GetExtension() != extension {
		return fmt.Errorf("extension must be %q", extension)
	}
	if target.GetMimeType() != mimeType {
		return fmt.Errorf("mime_type must be %q", mimeType)
	}
	if target.GetDisposition() != commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE || target.DownloadFilename != nil {
		return fmt.Errorf("derivative asset must use inline disposition without a download filename")
	}
	expectedKey, err := mediaauth.AssetObjectKey(target.GetAssetId(), extension)
	if err != nil {
		return err
	}
	if target.GetObjectKey() != expectedKey {
		return fmt.Errorf("object_key must be %q", expectedKey)
	}
	return nil
}

func canonicalObjectKey(target *commonv1.MediaObjectTarget) (string, error) {
	extension := target.GetExtension()
	if extension == "" || extension != strings.ToLower(strings.TrimSpace(extension)) {
		return "", fmt.Errorf("extension is not canonical")
	}
	expectedKey, err := mediaauth.MediaObjectKey(target.GetFileId(), extension)
	if err != nil {
		return "", fmt.Errorf("extension is not canonical: %w", err)
	}
	return expectedKey, nil
}

func validateCanonicalMIME(mimeType string, expectedPrefix string) error {
	if mimeType == "" || mimeType != strings.ToLower(strings.TrimSpace(mimeType)) || strings.Contains(mimeType, ";") {
		return fmt.Errorf("mime_type is not canonical")
	}
	if !strings.HasPrefix(mimeType, expectedPrefix) {
		return fmt.Errorf("mime_type %q must start with %q", mimeType, expectedPrefix)
	}
	return nil
}
