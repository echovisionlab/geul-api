package middleware

import (
	"context"
	"errors"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mq"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
)

// Recovery catches panics and converts them to errors
func Recovery() mq.Middleware {
	return func(next mq.Handler) mq.Handler {
		return func(ctx context.Context, msg mq.Message) (err error) {
			defer func() {
				if value := recover(); value != nil {
					var private []string
					if msg.Queue == eventpkg.QueueEmailSend || msg.Queue == eventpkg.QueueEmailAuth {
						var job managev1.SendEmailEvent
						_ = proto.Unmarshal(msg.Body, &job)
						private = append(private, job.GetRecipient())
						for _, field := range job.GetTemplateData() {
							private = append(private, field)
						}
					}
					cause, stack := failurecause.Panic(value, private...)
					slog.ErrorContext(ctx, "Queue handler panic", "queue", msg.Queue, "message_id", msg.MessageID, "command_id", msg.CorrelationID, "failure_cause", cause, "panic_stack", stack)
					err = errors.New("queue handler panic")
				}
			}()
			return next(ctx, msg)
		}
	}
}
