package handler

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/transcoding/hls"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type audioProcessor struct {
	coordinator *jobCoordinator
	sources     *sourcePreparer
	generator   AudioGenerator
	completions *completionService
	storage     StorageClient
	bitrate     string
}

func (p *audioProcessor) process(ctx context.Context, job *apiv1.TranscodeAudioEvent) error {
	return (transcodeWorkflow[*apiv1.TranscodeAudioEvent]{
		validate:    validateAudioJob,
		start:       p.start,
		transcode:   p.transcode,
		fail:        p.fail,
		completions: p.completions,
		mediaType:   "audio",
	}).run(ctx, job)
}

func (p *audioProcessor) start(
	ctx context.Context,
	job *apiv1.TranscodeAudioEvent,
) (*transcodeSession, error) {
	return p.coordinator.beginTranscodeJob(
		ctx,
		job,
		job.GetHlsOutput().GetObjectPrefix()+"/"+hls.MasterManifestName,
		audioCompletionExpectation(job),
	)
}

func (p *audioProcessor) transcode(
	ctx context.Context,
	job *apiv1.TranscodeAudioEvent,
	reportProgress progressReporter,
	startedAt time.Time,
) (*apiv1.TranscodeCompleteEvent, error) {
	source, err := p.sources.prepareJobSource(
		ctx,
		job.GetEventId(),
		job.GetSource().GetObjectKey(),
		reportProgress,
		p.sources.inspector.ValidateAudioFile,
	)
	if err != nil {
		return nil, err
	}
	defer p.sources.cleanupJobWorkDir(job.GetEventId())

	transcodeCtx, cancel := p.sources.withTranscodeTimeout(ctx, job.GetEventId(), source.probe.DurationSeconds)
	defer cancel()
	outputs, segmentDir, err := p.generateOutputs(transcodeCtx, job, source, reportProgress)
	if err != nil {
		return nil, err
	}
	complete := newTranscodeCompletion(job, apiv1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO, outputs, startedAt)
	if err := p.completions.uploadHLS(
		transcodeCtx,
		job.GetHlsOutput(),
		segmentDir,
		complete,
		reportProgress,
		"audio",
	); err != nil {
		return nil, err
	}
	return complete, nil
}

func (p *audioProcessor) generateOutputs(
	ctx context.Context,
	job *apiv1.TranscodeAudioEvent,
	source *preparedSource,
	reportProgress progressReporter,
) (*apiv1.TranscodeOutputs, string, error) {
	duration := source.probe.DurationSeconds
	spectrogram, err := p.generateSpectrogram(
		ctx, source.workDir, source.path, job.GetSpectrogramOutput(), duration, reportProgress,
	)
	if err != nil {
		return nil, "", err
	}
	audioHLS, err := p.generateAudioHLS(ctx, source.workDir, source.path, duration, reportProgress)
	if err != nil {
		return nil, "", err
	}
	return &apiv1.TranscodeOutputs{
		DurationSeconds: ptrInt32(int32(duration)),
		Spectrogram:     spectrogram,
	}, audioHLS.SegmentDir, nil
}
