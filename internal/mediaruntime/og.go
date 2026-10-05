package mediaruntime

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/echovisionlab/geul-api/internal/config"
)

// OGProcess runs the original Node worker unchanged. Its existing queue leases,
// retries, RPC authentication, rendering and graceful drain remain in media/og.
type OGProcess struct {
	cmd       *exec.Cmd
	healthURL string
	client    *http.Client
	mu        sync.Mutex
	done      chan struct{}
	stopping  bool
}

func NewOGProcess(cfg *config.Config) (*OGProcess, error) {
	script, err := filepath.Abs(cfg.Media.OGScriptPath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(script); err != nil {
		return nil, fmt.Errorf("OG worker script: %w", err)
	}
	nodePath := cfg.Media.OGNodePath
	if nodePath == "" {
		nodePath = cfg.Media.NodePath
	}
	cmd := exec.Command(nodePath, script)
	cmd.Dir = filepath.Dir(filepath.Dir(script))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	// Cmd.Environ preserves the deployment's telemetry configuration. Explicit
	// values below override the API port and point existing RPC calls to loopback.
	cmd.Env = append(cmd.Environ(),
		"HOST=127.0.0.1", "PORT="+strconv.Itoa(cfg.Media.OGPort),
		"OG_GENERATE_WORKERS="+strconv.Itoa(cfg.Media.OGWorkers),
		"OG_SHUTDOWN_TIMEOUT_MS="+strconv.Itoa(cfg.Media.OGShutdownTimeoutMS),
		"DATABASE_DSN="+cfg.DatabaseDSN, "S3_ENDPOINT="+cfg.S3Endpoint,
		"S3_MEDIA_BUCKET="+cfg.S3Bucket, "S3_REGION="+cfg.S3Region,
		"S3_ACCESS_KEY_ID="+cfg.S3AccessKeyID, "S3_SECRET_ACCESS_KEY="+cfg.S3SecretAccessKey,
		"TOKEN_SIGNING_SECRET="+cfg.TokenSigningSecret,
		"BACKEND_URL=http://127.0.0.1:"+strconv.Itoa(cfg.Port),
	)
	if cfg.Media.OGTelemetryAttributes != "" {
		cmd.Env = append(cmd.Env, "OTEL_RESOURCE_ATTRIBUTES="+cfg.Media.OGTelemetryAttributes)
	}
	return &OGProcess{cmd: cmd, healthURL: fmt.Sprintf("http://127.0.0.1:%d/health", cfg.Media.OGPort), client: &http.Client{Timeout: 2 * time.Second}}, nil
}

// Start is called only after the API's HTTP listener has bound successfully.
func (p *OGProcess) Start(onFailure func(error)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("start OG worker: %w", err)
	}
	p.done = make(chan struct{})
	go func() {
		err := p.cmd.Wait()
		p.mu.Lock()
		stopping := p.stopping
		close(p.done)
		p.mu.Unlock()
		if !stopping {
			onFailure(fmt.Errorf("OG worker exited unexpectedly: %v", err))
		}
	}()
	return nil
}

func (p *OGProcess) Ready(ctx context.Context) error {
	p.mu.Lock()
	done, stopping := p.done, p.stopping
	p.mu.Unlock()
	if done == nil || stopping {
		return fmt.Errorf("OG worker is not running")
	}
	select {
	case <-done:
		return fmt.Errorf("OG worker exited")
	default:
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.healthURL, nil)
	if err != nil {
		return err
	}
	res, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("OG readiness: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("OG worker is not ready: %d", res.StatusCode)
	}
	return nil
}

// Shutdown must precede closing API HTTP: the existing worker commits results
// through internal RPC while draining. Kill only after the shutdown deadline.
func (p *OGProcess) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	done := p.done
	if done == nil {
		p.mu.Unlock()
		return nil
	}
	if !p.stopping {
		p.stopping = true
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
	}
	p.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		_ = p.cmd.Process.Kill()
		<-done
		return ctx.Err()
	}
}
