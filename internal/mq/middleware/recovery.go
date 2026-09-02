package middleware

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/mq"
)

// Recovery catches panics and converts them to errors
func Recovery() mq.Middleware {
	return func(next mq.Handler) mq.Handler {
		return func(ctx context.Context, msg mq.Message) (err error) {
			defer func() {
				if recover() != nil {
					err = errors.New("queue handler panic")
				}
			}()
			return next(ctx, msg)
		}
	}
}
