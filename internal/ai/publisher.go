package ai

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mq"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// AsyncPublisher is the transport capability consumed by AI handlers and
// metadata jobs. Durable metadata work must also implement the transactional
// extension below so the job and command commit atomically.
type AsyncPublisher interface {
	EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error
}

func publishDurableProtoInTransaction(
	ctx context.Context,
	publisher AsyncPublisher,
	tx *gorm.DB,
	queue string,
	messageID string,
	message proto.Message,
) error {
	executor, err := mq.GormTransactionExecutor(tx)
	if err != nil {
		return err
	}
	return publisher.EnqueueProtobufWithExecutor(
		ctx,
		executor,
		queue,
		messageID,
		message,
	)
}
