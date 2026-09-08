package proxy

import (
	"context"
	"log/slog"
	"time"
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
			slog.Error("failed to cache "+resource, append(attributes, "error", err)...)
			return
		}
		slog.Debug("cached "+resource, attributes...)
	})
}
