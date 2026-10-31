package middleware

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mq"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
)

// Recovery catches panics and converts them to errors. The returned error
// carries the sanitized panic value and stack, so the consumer's single
// "PGMQ handler failed" record holds them.
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
					err = failurecause.WithAttrs(
						failurecause.WithPrivate(fmt.Errorf("queue handler panic: %s", cause), private...),
						slog.String("panic_stack", stack),
					)
				}
			}()
			return next(ctx, msg)
		}
	}
}
