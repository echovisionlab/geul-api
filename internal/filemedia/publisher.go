package filemedia

import (
	"context"
	"fmt"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// AsyncPublisher is the File lifecycle command and signal boundary.
type AsyncPublisher interface {
	EnqueueProtobuf(context.Context, string, string, proto.Message) error
	NotifyProtobuf(context.Context, string, proto.Message) error
}

// TransactionalAsyncPublisher atomically enqueues a File command with domain state.
type TransactionalAsyncPublisher interface {
	AsyncPublisher
	EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error
}

func publishDurableProto(ctx context.Context, publisher AsyncPublisher, queue, messageID string, message proto.Message) error {
	if publisher == nil {
		return fmt.Errorf("async publisher is required")
	}
	return publisher.EnqueueProtobuf(ctx, queue, messageID, message)
}

func publishDurableProtoInTransaction(
	ctx context.Context,
	publisher AsyncPublisher,
	tx *gorm.DB,
	queue string,
	messageID string,
	message proto.Message,
) error {
	if publisher == nil {
		return fmt.Errorf("async publisher is required")
	}
	if tx == nil || tx.Statement == nil || tx.Statement.ConnPool == nil {
		return fmt.Errorf("database transaction is required")
	}
	executor, ok := tx.Statement.ConnPool.(eventpkg.DBTX)
	if !ok {
		return fmt.Errorf("database transaction does not expose a PGMQ executor")
	}
	transactional, ok := publisher.(TransactionalAsyncPublisher)
	if !ok {
		return fmt.Errorf("async publisher does not support transactional PGMQ enqueue")
	}
	return transactional.EnqueueProtobufWithExecutor(ctx, executor, queue, messageID, message)
}

func publishSignalProto(ctx context.Context, publisher AsyncPublisher, signal string, message proto.Message) error {
	if publisher == nil {
		return fmt.Errorf("async publisher is required")
	}
	return publisher.NotifyProtobuf(ctx, signal, message)
}
