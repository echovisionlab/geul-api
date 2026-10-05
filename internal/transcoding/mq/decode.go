package mq

import (
	"context"
	"fmt"

	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/proto"
)

// AudioJobHandler handles a decoded audio command.
type AudioJobHandler func(context.Context, *apiv1.TranscodeAudioEvent) error

// VideoJobHandler handles a decoded video command.
type VideoJobHandler func(context.Context, *apiv1.TranscodeVideoEvent) error

// WaveformJobHandler handles a decoded waveform command.
type WaveformJobHandler func(context.Context, *apiv1.WaveformGenerateEvent) error

// DecodeAudioJob adapts an audio handler to the raw message consumer boundary.
func DecodeAudioJob(handle AudioJobHandler) Handler {
	return func(ctx context.Context, body []byte) error {
		job, err := ParseAudioJob(body)
		if err != nil {
			return err
		}
		return handle(ctx, job)
	}
}

// DecodeVideoJob adapts a video handler to the raw message consumer boundary.
func DecodeVideoJob(handle VideoJobHandler) Handler {
	return func(ctx context.Context, body []byte) error {
		job, err := ParseVideoJob(body)
		if err != nil {
			return err
		}
		return handle(ctx, job)
	}
}

// DecodeWaveformJob adapts a waveform handler to the raw message consumer boundary.
func DecodeWaveformJob(handle WaveformJobHandler) Handler {
	return func(ctx context.Context, body []byte) error {
		var job apiv1.WaveformGenerateEvent
		if err := proto.Unmarshal(body, &job); err != nil {
			return fmt.Errorf("failed to parse waveform job: %w", err)
		}
		return handle(ctx, &job)
	}
}

// ParseAudioJob parses a protobuf audio command.
func ParseAudioJob(body []byte) (*apiv1.TranscodeAudioEvent, error) {
	var job apiv1.TranscodeAudioEvent
	if err := proto.Unmarshal(body, &job); err != nil {
		return nil, fmt.Errorf("failed to parse audio job: %w", err)
	}
	return &job, nil
}

// ParseVideoJob parses a protobuf video command.
func ParseVideoJob(body []byte) (*apiv1.TranscodeVideoEvent, error) {
	var job apiv1.TranscodeVideoEvent
	if err := proto.Unmarshal(body, &job); err != nil {
		return nil, fmt.Errorf("failed to parse video job: %w", err)
	}
	return &job, nil
}
