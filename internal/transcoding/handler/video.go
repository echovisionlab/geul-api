package handler

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/transcoding/hls"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type videoProcessor struct {
	coordinator *jobCoordinator
	sources     *sourcePreparer
	generator   VideoGenerator
	completions *completionService
	storage     StorageClient
}

func (p *videoProcessor) process(ctx context.Context, job *apiv1.TranscodeVideoEvent) error {
	return (transcodeWorkflow[*apiv1.TranscodeVideoEvent]{
		validate:    validateVideoJob,
		start:       p.start,
		transcode:   p.transcode,
		fail:        p.fail,
		completions: p.completions,
		mediaType:   "video",
	}).run(ctx, job)
}

func (p *videoProcessor) start(
	ctx context.Context,
	job *apiv1.TranscodeVideoEvent,
) (*transcodeSession, error) {
	return p.coordinator.beginTranscodeJob(
		ctx,
		job,
		job.GetHlsOutput().GetObjectPrefix()+"/"+hls.MasterManifestName,
		videoCompletionExpectation(job),
	)
}

func (p *videoProcessor) transcode(
	ctx context.Context,
	job *apiv1.TranscodeVideoEvent,
	reportProgress progressReporter,
	startedAt time.Time,
) (*apiv1.TranscodeCompleteEvent, error) {
	source, err := p.sources.prepareJobSource(
		ctx,
		job.GetEventId(),
		job.GetSource().GetObjectKey(),
		reportProgress,
		p.sources.inspector.ValidateVideoFile,
	)
	if err != nil {
		return nil, err
	}
	defer p.sources.cleanupJobWorkDir(job.GetEventId())

	transcodeCtx, cancel := p.sources.withTranscodeTimeout(ctx, job.GetEventId(), source.probe.DurationSeconds*1.5)
	defer cancel()
	outputs, segmentDir, err := p.generateOutputs(transcodeCtx, job, source, reportProgress)
	if err != nil {
		return nil, err
	}
	complete := newTranscodeCompletion(job, apiv1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO, outputs, startedAt)
	if err := p.completions.uploadHLS(
		transcodeCtx,
		job.GetHlsOutput(),
		segmentDir,
		complete,
		reportProgress,
		"video",
	); err != nil {
		return nil, err
	}
	return complete, nil
}

func (p *videoProcessor) generateOutputs(
	ctx context.Context,
	job *apiv1.TranscodeVideoEvent,
	source *preparedSource,
	reportProgress progressReporter,
) (*apiv1.TranscodeOutputs, string, error) {
	duration := source.probe.DurationSeconds
	generateThumbnail, resolutions := videoOptions(job)
	thumbnail, err := p.generateVideoThumbnail(
		ctx, source.workDir, source.path, job.GetThumbnailOutput(), duration, generateThumbnail, reportProgress,
	)
	if err != nil {
		return nil, "", err
	}
	videoHLS, err := p.generateVideoHLS(
		ctx, job.GetEventId(), source.workDir, source.path, resolutions, duration, reportProgress,
	)
	if err != nil {
		return nil, "", err
	}
	return &apiv1.TranscodeOutputs{
		DurationSeconds: ptrInt32(int32(duration)),
		Thumbnail:       thumbnail,
	}, videoHLS.SegmentDir, nil
}
