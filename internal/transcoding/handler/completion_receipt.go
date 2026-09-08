package handler

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/transcoding/jobresult"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/proto"
)

func (s *completionService) replay(
	ctx context.Context,
	command transcodeCommand,
	key string,
	expected transcodeCompletionExpectation,
) (bool, error) {
	payload, found, err := s.storage.Completion(ctx, key)
	if err != nil {
		return false, jobresult.Retry(fmt.Errorf("inspect transcode completion: %w", err))
	}
	if !found {
		return false, nil
	}
	var complete apiv1.TranscodeCompleteEvent
	if err := proto.Unmarshal(payload, &complete); err != nil {
		return false, fmt.Errorf("decode transcode completion: %w", err)
	}
	if !matchesTranscodeCompletion(command, &complete, expected) {
		return false, fmt.Errorf("transcode completion does not match command %s", command.GetEventId())
	}
	if err := s.publisher.PublishComplete(ctx, &complete); err != nil {
		return false, jobresult.Retry(fmt.Errorf("replay transcode completion: %w", err))
	}
	return true, nil
}

type transcodeCompletionExpectation struct {
	eventType    apiv1.TranscodeEventType
	generationID string
	assetID      string
}

func audioCompletionExpectation(command *apiv1.TranscodeAudioEvent) transcodeCompletionExpectation {
	return transcodeCompletionExpectation{
		eventType:    apiv1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		generationID: command.GetHlsOutput().GetGenerationId(),
		assetID:      command.GetSpectrogramOutput().GetAssetId(),
	}
}

func videoCompletionExpectation(command *apiv1.TranscodeVideoEvent) transcodeCompletionExpectation {
	expected := transcodeCompletionExpectation{
		eventType:    apiv1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO,
		generationID: command.GetHlsOutput().GetGenerationId(),
	}
	if command.GetOptions() == nil || command.GetOptions().GetGenerateThumbnail() {
		expected.assetID = command.GetThumbnailOutput().GetAssetId()
	}
	return expected
}

func matchesTranscodeCompletion(
	command transcodeCommand,
	complete *apiv1.TranscodeCompleteEvent,
	expected transcodeCompletionExpectation,
) bool {
	outputs := complete.GetOutputs()
	return complete.GetSuccess() &&
		complete.GetEventId() == command.GetEventId() &&
		complete.GetEventType() == expected.eventType &&
		complete.GetEntityType() == command.GetEntityType() &&
		complete.GetEntityId() == command.GetEntityId() &&
		complete.GetFileId() == command.GetFileId() &&
		outputs.GetHls().GetGenerationId() == expected.generationID &&
		(expected.assetID == "" || transcodeAssetID(outputs) == expected.assetID)
}

func transcodeAssetID(outputs *apiv1.TranscodeOutputs) string {
	if outputs.GetSpectrogram() != nil {
		return outputs.GetSpectrogram().GetAssetId()
	}
	return outputs.GetThumbnail().GetAssetId()
}
