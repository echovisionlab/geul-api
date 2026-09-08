package mediaruntime

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/stretchr/testify/require"
)

func startOGFixture(t *testing.T, shutdown string, backendPort int) (*OGProcess, <-chan error) {
	t.Helper()
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "dist"), 0700))
	script := filepath.Join(dir, "dist", "index.mjs")
	require.NoError(t, os.WriteFile(script, []byte(`import http from 'node:http';
 const server=http.createServer((req,res)=>{res.end('ok');});
 process.on('SIGTERM',async()=>{`+shutdown+`});
 server.listen(Number(process.env.PORT),process.env.HOST);
 `), 0600))
	cfg := &config.Config{Port: backendPort, Media: config.MediaConfig{NodePath: node, OGScriptPath: script, OGPort: port, OGWorkers: 1, OGShutdownTimeoutMS: 1000}}
	process, err := NewOGProcess(cfg)
	require.NoError(t, err)
	failures := make(chan error, 1)
	require.NoError(t, process.Start(func(err error) { failures <- err }))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = process.Shutdown(ctx)
	})
	require.Eventually(t, func() bool { return process.Ready(context.Background()) == nil }, 5*time.Second, 20*time.Millisecond)
	return process, failures
}

func TestOGProcessDrainsThroughAPIWithoutReportingCrash(t *testing.T) {
	committed := make(chan struct{}, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { committed <- struct{}{}; w.WriteHeader(http.StatusOK) }))
	defer backend.Close()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(backend.URL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	process, failures := startOGFixture(t, `await fetch(process.env.BACKEND_URL+'/complete',{method:'POST'});server.close(()=>process.exit(0));`, port)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, process.Shutdown(ctx))
	select {
	case <-committed:
	default:
		t.Fatal("worker did not commit its result while draining")
	}
	select {
	case err := <-failures:
		t.Fatalf("graceful exit reported as crash: %v", err)
	default:
	}
	require.Error(t, process.Ready(ctx))
	require.NoError(t, process.Shutdown(ctx))
}

func TestOGProcessReportsUnexpectedExit(t *testing.T) {
	process, failures := startOGFixture(t, `process.exit(0);`, 8000)
	require.NoError(t, process.cmd.Process.Kill())
	select {
	case err := <-failures:
		require.ErrorContains(t, err, "exited unexpectedly")
	case <-time.After(3 * time.Second):
		t.Fatal("worker crash was not reported")
	}
	require.Error(t, process.Ready(context.Background()))
}

func TestOGProcessKillsWorkerAtShutdownDeadline(t *testing.T) {
	process, _ := startOGFixture(t, `/* fixture deliberately refuses to stop */`, 8000)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, process.Shutdown(ctx), context.DeadlineExceeded)
	select {
	case <-process.done:
	default:
		t.Fatal("worker left alive after deadline")
	}
}
