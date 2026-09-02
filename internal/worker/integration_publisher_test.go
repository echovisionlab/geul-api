//go:build integration

package worker

import (
	"context"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
)

type recordingWorkerPublisher struct {
	sendEmailEvents                []*managev1.SendEmailEvent
	sendBulkEmailEvents            []*managev1.SendBulkEmailBatchEvent
	fileDeleteEvents               []*managev1.FileDeleteEvent
	mediaProcessingLifecycleEvents []*managev1.MediaProcessingLifecycleEvent
	transcodeCancelEvents          []*managev1.TranscodeCancelEvent
	waveformGenerateEvents         []*managev1.WaveformGenerateEvent
}

func (*recordingWorkerPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (*recordingWorkerPublisher) EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error {
	return nil
}

func (*recordingWorkerPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (p *recordingWorkerPublisher) PublishSendEmail(_ context.Context, event *managev1.SendEmailEvent) error {
	p.sendEmailEvents = append(p.sendEmailEvents, event)
	return nil
}

func (p *recordingWorkerPublisher) PublishSendBulkEmail(_ context.Context, event *managev1.SendBulkEmailBatchEvent) error {
	p.sendBulkEmailEvents = append(p.sendBulkEmailEvents, event)
	return nil
}

func (p *recordingWorkerPublisher) PublishFileDelete(_ context.Context, event *managev1.FileDeleteEvent) error {
	p.fileDeleteEvents = append(p.fileDeleteEvents, event)
	return nil
}

func (p *recordingWorkerPublisher) PublishMediaProcessingLifecycle(_ context.Context, event *managev1.MediaProcessingLifecycleEvent) error {
	p.mediaProcessingLifecycleEvents = append(p.mediaProcessingLifecycleEvents, event)
	return nil
}

func (p *recordingWorkerPublisher) PublishTranscodeCancel(_ context.Context, event *managev1.TranscodeCancelEvent) error {
	p.transcodeCancelEvents = append(p.transcodeCancelEvents, event)
	return nil
}

func (p *recordingWorkerPublisher) PublishWaveformGenerate(_ context.Context, event *managev1.WaveformGenerateEvent) error {
	p.waveformGenerateEvents = append(p.waveformGenerateEvents, event)
	return nil
}
