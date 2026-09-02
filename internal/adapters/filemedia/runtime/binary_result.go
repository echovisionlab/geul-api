package runtime

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type DecodedBinaryResult struct {
	StableID string
	Body     []byte
	Complete bool
}

func DecodeTranscodeResult(body []byte) (*DecodedBinaryResult, error) {
	var event managev1.TranscodeCompleteEvent
	if err := proto.Unmarshal(body, &event); err != nil {
		return nil, fmt.Errorf("invalid transcode result: %w", err)
	}
	if strings.TrimSpace(event.GetEventId()) == "" {
		return nil, fmt.Errorf("transcode result has no stable event id")
	}
	return &DecodedBinaryResult{StableID: event.GetEventId(), Body: body, Complete: true}, nil
}

func DecodeWaveformResult(body []byte) (*DecodedBinaryResult, error) {
	var result managev1.WaveformResultEvent
	if err := proto.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("invalid waveform result: %w", err)
	}
	switch value := result.GetOutcome().(type) {
	case *managev1.WaveformResultEvent_Completed:
		return marshalBinaryResult(value.Completed.GetEventId(), value.Completed, true)
	case *managev1.WaveformResultEvent_Failed:
		return marshalBinaryResult(value.Failed.GetEventId(), value.Failed, false)
	default:
		return nil, fmt.Errorf("waveform result outcome is required")
	}
}

func DecodeMeshOptimizationResult(body []byte) (*DecodedBinaryResult, error) {
	var result managev1.MeshOptimizationResultEvent
	if err := proto.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("invalid mesh optimization result: %w", err)
	}
	switch value := result.GetOutcome().(type) {
	case *managev1.MeshOptimizationResultEvent_Completed:
		return marshalBinaryResult(value.Completed.GetJobId(), value.Completed, true)
	case *managev1.MeshOptimizationResultEvent_Failed:
		return marshalBinaryResult(value.Failed.GetJobId(), value.Failed, false)
	default:
		return nil, fmt.Errorf("mesh optimization result outcome is required")
	}
}

func marshalBinaryResult(
	stableID string,
	outcome proto.Message,
	complete bool,
) (*DecodedBinaryResult, error) {
	if strings.TrimSpace(stableID) == "" {
		return nil, fmt.Errorf("binary result outcome has no stable id")
	}
	body, err := proto.Marshal(outcome)
	if err != nil {
		return nil, fmt.Errorf("marshal binary result outcome: %w", err)
	}
	return &DecodedBinaryResult{StableID: stableID, Body: body, Complete: complete}, nil
}
