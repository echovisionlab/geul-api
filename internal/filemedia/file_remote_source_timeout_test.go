package filemedia

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBrowserUploadSourceClientTimeouts(t *testing.T) {
	service := &FileService{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := service.newBrowserUploadSourceClient(ctx, cancel, browserUploadSourceIdleTimeout)
	defer client.CloseIdleConnections()
	require.Zero(t, client.Timeout)
	require.Equal(t, 120*time.Second, client.transport.ResponseHeaderTimeout)
	require.Equal(t, 10*time.Second, client.transport.TLSHandshakeTimeout)
	resolver := service.remoteImportResolver
	// The unchanged durable-import constructor still has its total cap.
	imported := newRemoteImportHTTPClient(ctx, resolver, nil)
	defer imported.CloseIdleConnections()
	require.Equal(t, 10*time.Minute, imported.Timeout)
}

func TestRemoteImportAndBrowserSourceOpenersUseSeparateTimeouts(t *testing.T) {
	service, _ := uploadSourceTestService(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(uploadSourcePNG(20))
	}), 100)
	ctx := uploadSourceTestRequest(t).Context()
	request, err := service.prepareRemoteImport(ctx, mustBrowserUploadSourceOptions(t))
	require.NoError(t, err)
	imported, err := service.openRemoteImportSource(ctx, request, nil, remoteImportFailureReporter(nil))
	require.NoError(t, err)
	require.Equal(t, 10*time.Minute, imported.client.Timeout)
	imported.close()
	browser, err := service.openBrowserUploadSource(ctx, request)
	require.NoError(t, err)
	require.Zero(t, browser.client.Timeout)
	require.Equal(t, 120*time.Second, browser.client.transport.ResponseHeaderTimeout)
	browser.close()
}

type browserSourceTestBody struct {
	ctx    context.Context
	closed bool
	wait   bool
}

func (b *browserSourceTestBody) Read(p []byte) (int, error) {
	if b.wait {
		<-b.ctx.Done()
		return 0, b.ctx.Err()
	}
	p[0] = 'x'
	return 1, nil
}
func (b *browserSourceTestBody) Close() error { b.closed = true; return nil }

func TestBrowserUploadSourceBlockedReadCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	underlying := &browserSourceTestBody{ctx: ctx, wait: true}
	body := &browserUploadSourceBody{ReadCloser: underlying, cancel: cancel, idleTimeout: 20 * time.Millisecond}
	_, err := body.Read(make([]byte, 1))
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, body.Close())
	require.True(t, underlying.closed)
}

func TestBrowserUploadSourceBackpressureDoesNotConsumeReadIdleBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	underlying := &browserSourceTestBody{ctx: ctx}
	body := &browserUploadSourceBody{ReadCloser: underlying, cancel: cancel, idleTimeout: 10 * time.Millisecond}
	for range 3 {
		n, err := body.Read(make([]byte, 1))
		require.NoError(t, err)
		require.Equal(t, 1, n)
		// No pending source read while downstream disk/network work takes place.
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			t.Fatal("idle budget ran between reads")
		}
	}
	require.NoError(t, ctx.Err())
	final := &browserUploadSourceCloseBody{ReadCloser: body, cancel: cancel}
	require.NoError(t, final.Close())
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.True(t, underlying.closed)
}

func TestBrowserUploadSourceIdleLimitAppliesBeforeSniff(t *testing.T) {
	service, _ := uploadSourceTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}), 100)
	ctx, cancel := context.WithCancel(uploadSourceTestRequest(t).Context())
	defer cancel()
	request, err := service.prepareRemoteImport(ctx, mustBrowserUploadSourceOptions(t))
	require.NoError(t, err)
	client := service.newBrowserUploadSourceClient(ctx, cancel, 20*time.Millisecond)
	source, err := service.openRemoteImportSourceWithClient(ctx, request, nil, remoteImportFailureReporter(nil), client)
	require.Error(t, err)
	require.Nil(t, source)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
}

func TestBrowserUploadSourceActiveReadsHaveNoTotalLimit(t *testing.T) {
	sourceBytes := uploadSourcePNG(remoteImportSniffBytes + 3)
	service, _ := uploadSourceTestService(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.(http.Flusher).Flush()
		// Total duration exceeds the injected 150ms idle budget; each read moves.
		for offset := 0; offset < len(sourceBytes); {
			end := min(offset+16*1024, len(sourceBytes))
			_, _ = w.Write(sourceBytes[offset:end])
			w.(http.Flusher).Flush()
			offset = end
			timer := time.NewTimer(60 * time.Millisecond)
			<-timer.C
		}
	}), int64(len(sourceBytes)+1))
	ctx, cancel := context.WithCancel(uploadSourceTestRequest(t).Context())
	defer cancel()
	request, err := service.prepareRemoteImport(ctx, mustBrowserUploadSourceOptions(t))
	require.NoError(t, err)
	client := service.newBrowserUploadSourceClient(ctx, cancel, 150*time.Millisecond)
	source, err := service.openRemoteImportSourceWithClient(ctx, request, nil, remoteImportFailureReporter(nil), client)
	require.NoError(t, err)
	defer source.close()
	remainder, err := io.ReadAll(source.body)
	require.NoError(t, err)
	require.Equal(t, sourceBytes, append(source.prefix, remainder...))
	require.NoError(t, ctx.Err())
}

func mustBrowserUploadSourceOptions(t *testing.T) remoteFileImportOptions {
	t.Helper()
	opts, err := parseUploadSourceQuery(uploadSourceTestRequest(t).URL.RawQuery)
	require.NoError(t, err)
	return opts
}
