package transcode

import (
	"fmt"
	"strings"

	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func ValidateAudioTranscodeEvent(event *managev1.TranscodeAudioEvent) error {
	if event == nil {
		return fmt.Errorf("audio transcode event is required")
	}
	if err := validateJobIdentity(event.GetEventId(), event.GetEntityType(), event.GetEntityId(), event.GetFileId()); err != nil {
		return fmt.Errorf("audio identity: %w", err)
	}
	if err := validateMediaObjectTarget(event.GetFileId(), event.GetSource()); err != nil {
		return fmt.Errorf("audio source: %w", err)
	}
	if err := validateMediaGenerationTarget(event.GetFileId(), event.GetHlsOutput()); err != nil {
		return fmt.Errorf("audio HLS output: %w", err)
	}
	if err := validateAssetTarget(event.GetSpectrogramOutput(), "png", "image/png"); err != nil {
		return fmt.Errorf("audio spectrogram output: %w", err)
	}
	return nil
}

func ValidateVideoTranscodeEvent(event *managev1.TranscodeVideoEvent) error {
	if event == nil {
		return fmt.Errorf("video transcode event is required")
	}
	if err := validateJobIdentity(event.GetEventId(), event.GetEntityType(), event.GetEntityId(), event.GetFileId()); err != nil {
		return fmt.Errorf("video identity: %w", err)
	}
	if err := validateMediaObjectTarget(event.GetFileId(), event.GetSource()); err != nil {
		return fmt.Errorf("video source: %w", err)
	}
	if err := validateMediaGenerationTarget(event.GetFileId(), event.GetHlsOutput()); err != nil {
		return fmt.Errorf("video HLS output: %w", err)
	}
	if err := validateAssetTarget(event.GetThumbnailOutput(), "webp", "image/webp"); err != nil {
		return fmt.Errorf("video thumbnail output: %w", err)
	}
	return nil
}

func ValidateWaveformGenerateEvent(event *managev1.WaveformGenerateEvent) error {
	if event == nil {
		return fmt.Errorf("waveform generate event is required")
	}
	if err := validateJobIdentity(event.GetEventId(), event.GetEntityType(), event.GetEntityId(), event.GetFileId()); err != nil {
		return fmt.Errorf("waveform identity: %w", err)
	}
	if err := validateMediaObjectTarget(event.GetFileId(), event.GetSource()); err != nil {
		return fmt.Errorf("waveform source: %w", err)
	}
	if err := validateAssetTarget(event.GetOutput(), "json", "application/json"); err != nil {
		return fmt.Errorf("waveform output: %w", err)
	}
	if event.GetEventId() != event.GetOutput().GetAssetId() {
		return fmt.Errorf("waveform event_id must match output asset_id")
	}
	return nil
}

func validateJobIdentity(
	eventID string,
	entityType managev1.TranscodeEntityType,
	entityID string,
	fileID string,
) error {
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
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE:
		return nil
	default:
		return fmt.Errorf("unsupported entity_type %s", entityType.String())
	}
}

func validateMediaObjectTarget(fileID string, target *commonv1.MediaObjectTarget) error {
	if target == nil {
		return fmt.Errorf("target is required")
	}
	if target.GetFileId() != fileID {
		return fmt.Errorf("file_id does not match event file_id")
	}
	extension := target.GetExtension()
	mimeType := target.GetMimeType()
	if extension == "" || extension != strings.ToLower(strings.TrimSpace(extension)) {
		return fmt.Errorf("extension is not canonical")
	}
	if mimeType == "" || mimeType != canonicalJobMimeType(mimeType) {
		return fmt.Errorf("mime_type is not canonical")
	}
	if expected := model.GetExtensionFromMime(mimeType); expected == "bin" || expected != extension {
		return fmt.Errorf("extension does not match verified mime_type")
	}
	expectedKey, err := mediaauth.MediaObjectKey(fileID, extension)
	if err != nil {
		return fmt.Errorf("invalid file_id or extension: %w", err)
	}
	if target.GetObjectKey() != expectedKey {
		return fmt.Errorf("object_key is not canonical")
	}
	return nil
}

func validateMediaGenerationTarget(fileID string, target *commonv1.MediaGenerationWriteTarget) error {
	if target == nil {
		return fmt.Errorf("target is required")
	}
	if target.GetFileId() != fileID {
		return fmt.Errorf("file_id does not match event file_id")
	}
	expectedPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, target.GetGenerationId())
	if err != nil {
		return fmt.Errorf("invalid file_id or generation_id: %w", err)
	}
	if target.GetObjectPrefix() != expectedPrefix {
		return fmt.Errorf("object_prefix is not canonical")
	}
	return nil
}

func validateAssetTarget(target *commonv1.AssetWriteTarget, extension string, mimeType string) error {
	if target == nil {
		return fmt.Errorf("target is required")
	}
	if target.GetExtension() != extension || target.GetMimeType() != mimeType {
		return fmt.Errorf("extension or mime_type does not match the allocated derivative")
	}
	if target.GetDisposition() != commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE || target.DownloadFilename != nil {
		return fmt.Errorf("derivative asset must use inline disposition")
	}
	expectedKey, err := mediaauth.AssetObjectKey(target.GetAssetId(), extension)
	if err != nil {
		return fmt.Errorf("invalid asset_id: %w", err)
	}
	if target.GetObjectKey() != expectedKey {
		return fmt.Errorf("object_key is not canonical")
	}
	return nil
}

func canonicalJobMimeType(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
}

func MatchTranscodeCompletionPayload(
	event *managev1.TranscodeCompleteEvent,
	payload []byte,
) (bool, error) {
	if event == nil || event.GetOutputs() == nil {
		return false, nil
	}
	switch event.GetEventType() {
	case managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO:
		var job managev1.TranscodeAudioEvent
		if err := proto.Unmarshal(payload, &job); err != nil {
			return false, fmt.Errorf("decode tracked audio transcode allocation: %w", err)
		}
		if err := ValidateAudioTranscodeEvent(&job); err != nil {
			return false, fmt.Errorf("validate tracked audio transcode allocation: %w", err)
		}
		if !completionIdentityMatches(event, job.GetEntityType(), job.GetEntityId(), job.GetFileId()) {
			return false, nil
		}
		return event.GetOutputs().GetHls().GetGenerationId() == job.GetHlsOutput().GetGenerationId() &&
			event.GetOutputs().GetSpectrogram().GetAssetId() == job.GetSpectrogramOutput().GetAssetId(), nil
	case managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO:
		var job managev1.TranscodeVideoEvent
		if err := proto.Unmarshal(payload, &job); err != nil {
			return false, fmt.Errorf("decode tracked video transcode allocation: %w", err)
		}
		if err := ValidateVideoTranscodeEvent(&job); err != nil {
			return false, fmt.Errorf("validate tracked video transcode allocation: %w", err)
		}
		if !completionIdentityMatches(event, job.GetEntityType(), job.GetEntityId(), job.GetFileId()) {
			return false, nil
		}
		return event.GetOutputs().GetHls().GetGenerationId() == job.GetHlsOutput().GetGenerationId() &&
			event.GetOutputs().GetThumbnail().GetAssetId() == job.GetThumbnailOutput().GetAssetId(), nil
	default:
		return false, nil
	}
}

func completionIdentityMatches(
	event *managev1.TranscodeCompleteEvent,
	entityType managev1.TranscodeEntityType,
	entityID string,
	fileID string,
) bool {
	return event.GetEntityType() == entityType && event.GetEntityId() == entityID && event.GetFileId() == fileID
}
