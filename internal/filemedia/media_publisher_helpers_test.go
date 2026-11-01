package filemedia

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/adapters/filemediaqueue"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"

	"google.golang.org/protobuf/proto"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type hardCutPublishedMessage struct {
	exchange    string
	key         string
	messageID   string
	messageType string
	body        []byte
}

type hardCutAsyncPublisher struct {
	messages []hardCutPublishedMessage
}

func (p *hardCutAsyncPublisher) EnqueueProtobuf(
	_ context.Context,
	queue string,
	messageID string,
	msg proto.Message,
) error {
	body, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(msg)
	p.messages = append(p.messages, hardCutPublishedMessage{
		key:         queue,
		messageID:   messageID,
		messageType: string(msg.ProtoReflect().Descriptor().FullName()),
		body:        body,
	})
	return nil
}

func (p *hardCutAsyncPublisher) NotifyProtobuf(
	_ context.Context,
	signal string,
	msg proto.Message,
) error {
	body, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(msg)
	p.messages = append(p.messages, hardCutPublishedMessage{
		exchange:    signal,
		messageType: string(msg.ProtoReflect().Descriptor().FullName()),
		body:        body,
	})
	return nil
}

func (p *hardCutAsyncPublisher) EnqueueProtobufWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	queue string,
	messageID string,
	msg proto.Message,
) error {
	if executor == nil {
		return errors.New("transactional fixture executor is required")
	}
	return p.EnqueueProtobuf(ctx, queue, messageID, msg)
}

func (p *hardCutAsyncPublisher) PublishIngest(ctx context.Context, event domain.IngestEvent) error {
	return filemediaqueue.Lifecycle(p).PublishIngest(ctx, event)
}
func (p *hardCutAsyncPublisher) PublishProcessing(ctx context.Context, event domain.ProcessingEvent) error {
	return filemediaqueue.Lifecycle(p).PublishProcessing(ctx, event)
}
