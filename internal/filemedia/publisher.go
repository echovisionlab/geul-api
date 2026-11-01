package filemedia

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"
)

// AsyncPublisher is the File lifecycle command and signal boundary.
type AsyncPublisher interface {
	EnqueueProtobuf(context.Context, string, string, proto.Message) error
	NotifyProtobuf(context.Context, string, proto.Message) error
}

func publishDurableProto(ctx context.Context, publisher AsyncPublisher, queue, messageID string, message proto.Message) error {
	if publisher == nil {
		return fmt.Errorf("async publisher is required")
	}
	return publisher.EnqueueProtobuf(ctx, queue, messageID, message)
}

func publishSignalProto(ctx context.Context, publisher AsyncPublisher, signal string, message proto.Message) error {
	if publisher == nil {
		return fmt.Errorf("async publisher is required")
	}
	return publisher.NotifyProtobuf(ctx, signal, message)
}
