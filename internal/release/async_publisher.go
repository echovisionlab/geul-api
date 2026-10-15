package release

import (
	"context"

	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
)

// AsyncPublisher deliberately exposes product-level transport semantics only:
// durable commands and ephemeral wake-up signals. content.updated is scheduled
// only through the transactional signal method.
type AsyncPublisher interface {
	EnqueueProtobuf(context.Context, string, string, proto.Message) error
	NotifyProtobuf(context.Context, string, proto.Message) error
	contentsignal.Publisher
}
