package filemedia

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"
)

const browserUploadSourceIdleTimeout = 120 * time.Second

// openBrowserUploadSource allows arbitrarily long active downloads, while the
// durable server import opener retains its existing total timeout.
func (s *FileService) openBrowserUploadSource(ctx context.Context, request preparedRemoteImport) (*remoteImportSource, error) {
	ctx, cancel := context.WithCancel(ctx)
	client := s.newBrowserUploadSourceClient(ctx, cancel, browserUploadSourceIdleTimeout)
	source, err := s.openRemoteImportSourceWithClient(ctx, request, nil, remoteImportFailureReporter(nil), client)
	if err != nil {
		cancel()
		return nil, err
	}
	source.response.Body = &browserUploadSourceCloseBody{ReadCloser: source.response.Body, cancel: cancel}
	return source, nil
}

func (s *FileService) newBrowserUploadSourceClient(ctx context.Context, cancel context.CancelFunc, idleTimeout time.Duration) *remoteImportHTTPClient {
	resolver := s.remoteImportResolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dial := s.remoteImportDialer
	if dial == nil {
		dial = (&net.Dialer{Timeout: 30 * time.Second}).DialContext
	}
	client := newRemoteImportHTTPClient(ctx, resolver, dial, s.remoteImportBaseTransport)
	client.Timeout = 0
	client.transport.ResponseHeaderTimeout = idleTimeout
	if client.transport.TLSHandshakeTimeout == 0 || client.transport.TLSHandshakeTimeout > 10*time.Second {
		client.transport.TLSHandshakeTimeout = 10 * time.Second
	}
	client.Transport = &browserUploadSourceTransport{transport: client.transport, cancel: cancel, idleTimeout: idleTimeout}
	return client
}

type browserUploadSourceTransport struct {
	transport   http.RoundTripper
	cancel      context.CancelFunc
	idleTimeout time.Duration
}

func (t *browserUploadSourceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.transport.RoundTrip(request)
	if err == nil {
		response.Body = &browserUploadSourceBody{ReadCloser: response.Body, cancel: t.cancel, idleTimeout: t.idleTimeout}
	}
	return response, err
}

// The timer exists only during a blocking read. Backpressure between reads
// (including browser disk writes) must not consume the source's idle budget.
type browserUploadSourceBody struct {
	io.ReadCloser
	cancel      context.CancelFunc
	idleTimeout time.Duration
}

func (b *browserUploadSourceBody) Read(p []byte) (int, error) {
	timer := time.AfterFunc(b.idleTimeout, b.cancel)
	defer timer.Stop()
	return b.ReadCloser.Read(p)
}

// Only the final response owns request cancellation. net/http also closes
// intermediate redirect bodies, which must not cancel the next request.
type browserUploadSourceCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *browserUploadSourceCloseBody) Close() error {
	b.cancel()
	return b.ReadCloser.Close()
}
