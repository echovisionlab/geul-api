package filemedia

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/stretchr/testify/require"
)

func uploadDeadlineTestServer(t *testing.T, handler http.Handler, hard time.Duration) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(httpsnoop.Wrap(w, httpsnoop.Hooks{}), r)
	}))
	server.Config.ReadTimeout = hard
	server.Config.WriteTimeout = hard
	server.Start()
	t.Cleanup(server.Close)
	return server
}

// Both cases use the same 100ms server limits and 300ms request stream.
// The idle wrapper additionally permits processing after EOF longer than idle,
// and a response stream longer than the original fixed write limit.
func TestUploadIdleTimeoutActiveStreams(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprintf("idle_wrapper_%t", wrapped), func(t *testing.T) {
			const hard = 100 * time.Millisecond
			const idle = 500 * time.Millisecond
			handler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "body timed out", http.StatusRequestTimeout)
					return
				}
				time.Sleep(idle + 100*time.Millisecond)
				for _, b := range body {
					if _, err := w.Write([]byte{b}); err != nil {
						return
					}
					if err := http.NewResponseController(w).Flush(); err != nil {
						return
					}
					time.Sleep(60 * time.Millisecond)
				}
			}))
			if wrapped {
				handler = withUploadIdleTimeout(handler, idle)
			}
			server := uploadDeadlineTestServer(t, handler, hard)
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			producerDone := make(chan struct{})
			go func() {
				defer close(producerDone)
				defer writer.Close()
				for range 5 {
					if _, err := writer.Write([]byte("a")); err != nil {
						return
					}
					time.Sleep(60 * time.Millisecond)
				}
			}()
			started := time.Now()
			response, err := server.Client().Post(server.URL, "application/octet-stream", reader)
			if !wrapped {
				if err == nil {
					response.Body.Close()
					require.NotEqual(t, http.StatusOK, response.StatusCode)
				}
				reader.Close()
				<-producerDone
				t.Logf("fixed 100ms deadline rejected 300ms upload after %s", time.Since(started))
				return
			}
			require.NoError(t, err)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, "aaaaa", string(body))
			<-producerDone
			t.Logf("500ms idle deadline completed identical upload, 600ms processing and 300ms response after %s", time.Since(started))
		})
	}
}

func TestUploadIdleTimeoutStoppedBody(t *testing.T) {
	result := make(chan error, 1)
	server := uploadDeadlineTestServer(t, withUploadIdleTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		result <- err
	}), 250*time.Millisecond), 100*time.Millisecond)
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	require.NoError(t, err)
	defer conn.Close()
	_, err = fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 2\r\n\r\na")
	require.NoError(t, err)
	select {
	case err := <-result:
		var timeout net.Error
		require.ErrorAs(t, err, &timeout)
		require.True(t, timeout.Timeout())
	case <-time.After(3 * time.Second):
		t.Fatal("stopped upload body did not hit idle deadline")
	}
}

func TestUploadIdleTimeoutStoppedResponseReader(t *testing.T) {
	result := make(chan error, 1)
	server := uploadDeadlineTestServer(t, withUploadIdleTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte("a"), 64*1024)
		for range 1024 {
			if _, err := w.Write(chunk); err != nil {
				result <- err
				return
			}
		}
		result <- fmt.Errorf("64MiB response unexpectedly fit without client reads")
	}), 250*time.Millisecond), 100*time.Millisecond)
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.(*net.TCPConn).SetReadBuffer(1024))
	_, err = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: test\r\n\r\n")
	require.NoError(t, err)
	// Receive the headers, then stop consuming the response while the server
	// fills the socket. This exercises a blocked network Write, not a fake writer.
	_, err = http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	select {
	case err := <-result:
		var timeout net.Error
		require.ErrorAs(t, err, &timeout)
		require.True(t, timeout.Timeout())
	case <-time.After(3 * time.Second):
		t.Fatal("stopped response reader did not hit idle deadline")
	}
}

func TestUploadIdleTimeoutRequiresDeadlineSupport(t *testing.T) {
	called := false
	handler := WithUploadIdleTimeout(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.False(t, called, "unsupported writers must not silently disable production deadlines")
}
