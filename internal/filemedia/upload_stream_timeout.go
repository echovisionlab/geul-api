package filemedia

import (
	"io"
	"log/slog"
	"net/http"
	"time"
)

const uploadIdleTimeout = 120 * time.Second

// WithUploadIdleTimeout replaces fixed HTTP deadlines with streaming idle
// deadlines. Register it inside authentication so rejected requests retain the
// ordinary server deadlines.
func WithUploadIdleTimeout(next http.Handler) http.Handler {
	return withUploadIdleTimeout(next, uploadIdleTimeout)
}

func withUploadIdleTimeout(next http.Handler, idle time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		if err := controller.SetWriteDeadline(time.Time{}); err != nil {
			slog.ErrorContext(r.Context(), "Cannot configure upload write deadline", "error", err)
			http.Error(w, "Upload streaming unavailable", http.StatusInternalServerError)
			return
		}
		if err := controller.SetReadDeadline(time.Time{}); err != nil {
			slog.ErrorContext(r.Context(), "Cannot configure upload read deadline", "error", err)
			http.Error(w, "Upload streaming unavailable", http.StatusInternalServerError)
			return
		}
		r.Body = &uploadIdleBody{ReadCloser: r.Body, controller: controller, idle: idle}
		next.ServeHTTP(&uploadIdleWriter{ResponseWriter: w, controller: controller, idle: idle}, r)
	})
}

type uploadIdleBody struct {
	io.ReadCloser
	controller *http.ResponseController
	idle       time.Duration
}

func (b *uploadIdleBody) Read(p []byte) (int, error) {
	if err := b.controller.SetReadDeadline(time.Now().Add(b.idle)); err != nil {
		return 0, err
	}
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		if clearErr := b.controller.SetReadDeadline(time.Time{}); clearErr != nil {
			return n, clearErr
		}
	}
	return n, err
}

type uploadIdleWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	idle       time.Duration
}

func (w *uploadIdleWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *uploadIdleWriter) Write(p []byte) (int, error) {
	if err := w.controller.SetWriteDeadline(time.Now().Add(w.idle)); err != nil {
		return 0, err
	}
	return w.ResponseWriter.Write(p)
}

func (w *uploadIdleWriter) Flush() { _ = w.FlushError() }

func (w *uploadIdleWriter) FlushError() error {
	if err := w.controller.SetWriteDeadline(time.Now().Add(w.idle)); err != nil {
		return err
	}
	return w.controller.Flush()
}
