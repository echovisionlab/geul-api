package proxy

import (
	"context"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
)

const cacheWriteTimeout = 10 * time.Second

type cacheStore interface {
	Get(context.Context, string) ([]byte, string, error)
	Put(context.Context, string, []byte, string) error
}

func runInBackground(task func()) {
	go task()
}

func cacheResponseInBackground(
	runBackground func(func()),
	storage cacheStore,
	cacheKey string,
	data []byte,
	contentType string,
	resource string,
	attributes ...any,
) {
	runBackground(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cacheWriteTimeout)
		defer cancel()

		if err := storage.Put(ctx, cacheKey, data, contentType); err != nil {
			slog.ErrorContext(ctx, "failed to cache "+resource, append(attributes, failurecause.Attr(err))...)
			return
		}
		slog.Debug("cached "+resource, attributes...)
	})
}
