//go:build integration

package filemedia

import (
	"context"
	"errors"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
)

type fileIngestAttachmentTestPublisher struct {
	confirmed []fileIngestPublishedMessage
	signals   []string
	err       error
}

type fileIngestPublishedMessage struct {
	queue     string
	messageID string
	body      []byte
}

func (p *fileIngestAttachmentTestPublisher) NotifyProtobuf(
	_ context.Context, signal string, _ proto.Message,
) error {
	p.signals = append(p.signals, signal)
	return p.err
}

func (p *fileIngestAttachmentTestPublisher) EnqueueProtobuf(
	_ context.Context, queue, messageID string, message proto.Message,
) error {
	body, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	p.confirmed = append(p.confirmed, fileIngestPublishedMessage{queue: queue, messageID: messageID, body: body})
	return p.err
}

func (p *fileIngestAttachmentTestPublisher) EnqueueProtobufWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	queue string,
	messageID string,
	message proto.Message,
) error {
	if executor == nil {
		return errors.New("transactional fixture executor is required")
	}
	return p.EnqueueProtobuf(ctx, queue, messageID, message)
}
