package release

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"
)

// AsyncPublisher deliberately exposes product-level transport semantics only:
// durable commands and ephemeral wake-up signals.
type AsyncPublisher interface {
	EnqueueProtobuf(context.Context, string, string, proto.Message) error
	NotifyProtobuf(context.Context, string, proto.Message) error
}

func publishSignalProto(
	ctx context.Context,
	publisher AsyncPublisher,
	signal string,
	message proto.Message,
) error {
	if publisher == nil {
		return fmt.Errorf("async publisher is required")
	}
	return publisher.NotifyProtobuf(ctx, signal, message)
}
