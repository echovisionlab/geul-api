//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

const hookProxyControlPath = "/_integration/control/upstream"
const hookProxyRegistrationHeader = "Integration-Hook-Registration"

var ErrHookProxyClosed = errors.New("integration hook proxy is closed")

// HookProxy keeps a stable container-reachable hook origin while each test
// process owns its short-lived hook server. The control token must only be
// passed through the orchestrator's protected lease descriptor.
type HookProxy struct {
	server    *http.Server
	listener  net.Listener
	transport *http.Transport
	done      chan error

	localBaseURL  string
	dockerBaseURL string
	controlToken  string

	mu       sync.RWMutex
	upstream *url.URL
	revision uint64
	closed   bool

	closeOnce sync.Once
	closeErr  error
}

// StartHookProxy listens only on an ephemeral loopback port. dockerHostBase is
// an HTTP origin without a port, for example http://host.testcontainers.internal
// or http://host.docker.internal. Docker access to that host mapping remains the
// caller's responsibility.
func StartHookProxy(dockerHostBase string) (*HookProxy, error) {
	dockerBase, err := parseHookProxyDockerBase(dockerHostBase)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for integration hook proxy: %w", err)
	}
	token, err := newHookProxyControlToken()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("resolve integration hook proxy port: %w", err)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	proxy := &HookProxy{
		listener:      listener,
		transport:     transport,
		done:          make(chan error, 1),
		localBaseURL:  "http://" + listener.Addr().String(),
		dockerBaseURL: "http://" + net.JoinHostPort(dockerBase.Hostname(), port),
		controlToken:  token,
	}
	proxy.server = &http.Server{Handler: http.HandlerFunc(proxy.serveHTTP)}
	go func() {
		err := proxy.server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		proxy.done <- err
	}()
	return proxy, nil
}

// LocalBaseURL returns the loopback origin used by the protected control API.
func (proxy *HookProxy) LocalBaseURL() string {
	return proxy.localBaseURL
}

// DockerBaseURL returns the stable hook origin configured in Docker services.
func (proxy *HookProxy) DockerBaseURL() string {
	return proxy.dockerBaseURL
}

// ControlURL returns the protected endpoint used to register and unregister a
// test process's upstream hook origin.
func (proxy *HookProxy) ControlURL() string {
	return proxy.localBaseURL + hookProxyControlPath
}

// ControlToken returns the bearer token required by ControlURL.
func (proxy *HookProxy) ControlToken() string {
	return proxy.controlToken
}

// Register atomically replaces the active upstream hook origin. Integration
// packages are serialized, while replacement lets persistent runtime variants
// reactivate their own short-lived hook server before each test.
func (proxy *HookProxy) Register(upstreamBaseURL string) error {
	_, err := proxy.replace(upstreamBaseURL)
	return err
}

func (proxy *HookProxy) replace(upstreamBaseURL string) (uint64, error) {
	upstream, err := parseHookProxyUpstream(upstreamBaseURL)
	if err != nil {
		return 0, err
	}
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if proxy.closed {
		return 0, ErrHookProxyClosed
	}
	proxy.revision++
	proxy.upstream = upstream
	return proxy.revision, nil
}

// Unregister removes the active upstream. It is idempotent while the proxy is
// open so cleanup can safely run after a partially initialized test.
func (proxy *HookProxy) Unregister() error {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if proxy.closed {
		return ErrHookProxyClosed
	}
	proxy.upstream = nil
	return nil
}

func (proxy *HookProxy) unregisterRevision(rawRevision string) error {
	revision, err := strconv.ParseUint(strings.TrimSpace(rawRevision), 10, 64)
	if err != nil || revision == 0 {
		return fmt.Errorf("invalid integration hook registration")
	}
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if proxy.closed {
		return ErrHookProxyClosed
	}
	if proxy.revision == revision {
		proxy.upstream = nil
	}
	return nil
}

// Close stops accepting requests, cancels idle upstream connections, and waits
// for the serving goroutine. The caller controls the graceful-shutdown deadline.
func (proxy *HookProxy) Close(ctx context.Context) error {
	proxy.closeOnce.Do(func() {
		proxy.mu.Lock()
		proxy.closed = true
		proxy.upstream = nil
		proxy.mu.Unlock()

		shutdownErr := proxy.server.Shutdown(ctx)
		if shutdownErr != nil {
			shutdownErr = errors.Join(shutdownErr, proxy.server.Close())
		}
		proxy.transport.CloseIdleConnections()
		proxy.closeErr = errors.Join(shutdownErr, <-proxy.done)
	})
	return proxy.closeErr
}

func (proxy *HookProxy) serveHTTP(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path == hookProxyControlPath {
		proxy.serveControl(w, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/_integration/control/") {
		http.NotFound(w, request)
		return
	}
	proxy.forward(w, request)
}

func (proxy *HookProxy) serveControl(w http.ResponseWriter, request *http.Request) {
	if !proxy.authorized(request.Header.Get("Authorization")) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="integration-hook-proxy"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch request.Method {
	case http.MethodPut:
		request.Body = http.MaxBytesReader(w, request.Body, 4096)
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		var payload struct {
			UpstreamBaseURL string `json:"upstream_base_url"`
		}
		if err := decoder.Decode(&payload); err != nil {
			http.Error(w, "invalid control request", http.StatusBadRequest)
			return
		}
		if err := requireJSONEnd(decoder); err != nil {
			http.Error(w, "invalid control request", http.StatusBadRequest)
			return
		}
		revision, err := proxy.replace(payload.UpstreamBaseURL)
		if err != nil {
			switch {
			case errors.Is(err, ErrHookProxyClosed):
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
			default:
				http.Error(w, "invalid upstream hook URL", http.StatusBadRequest)
			}
			return
		}
		w.Header().Set(hookProxyRegistrationHeader, strconv.FormatUint(revision, 10))
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		revision := request.Header.Get(hookProxyRegistrationHeader)
		if strings.TrimSpace(revision) == "" {
			http.Error(w, "integration hook registration is required", http.StatusBadRequest)
			return
		}
		err := proxy.unregisterRevision(revision)
		if err != nil {
			if !errors.Is(err, ErrHookProxyClosed) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", http.MethodPut+", "+http.MethodDelete)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (proxy *HookProxy) forward(w http.ResponseWriter, request *http.Request) {
	proxy.mu.RLock()
	var upstream *url.URL
	if proxy.upstream != nil {
		copy := *proxy.upstream
		upstream = &copy
	}
	proxy.mu.RUnlock()
	if upstream == nil {
		http.Error(w, "integration hook upstream unavailable", http.StatusServiceUnavailable)
		return
	}

	upstream.Path, upstream.RawPath = joinHookProxyPath(upstream, request.URL)
	if upstream.RawQuery == "" || request.URL.RawQuery == "" {
		upstream.RawQuery += request.URL.RawQuery
	} else {
		upstream.RawQuery += "&" + request.URL.RawQuery
	}
	outbound, err := http.NewRequestWithContext(request.Context(), request.Method, upstream.String(), request.Body)
	if err != nil {
		http.Error(w, "integration hook upstream request invalid", http.StatusBadGateway)
		return
	}
	outbound.Header = request.Header.Clone()
	removeHopHeaders(outbound.Header)
	outbound.ContentLength = request.ContentLength

	response, err := (&http.Client{
		Transport: proxy.transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}).Do(outbound)
	if err != nil {
		http.Error(w, "integration hook upstream failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	responseHeaders := response.Header.Clone()
	removeHopHeaders(responseHeaders)
	copyHookProxyHeaders(w.Header(), responseHeaders)
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (proxy *HookProxy) authorized(header string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	provided := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if len(provided) != len(proxy.controlToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(proxy.controlToken)) == 1
}

func parseHookProxyDockerBase(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("parse integration hook Docker base: %w", err)
	}
	if parsed.Scheme != "http" || parsed.Hostname() == "" || parsed.Port() != "" ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("integration hook Docker base must be an HTTP origin without a port")
	}
	return parsed, nil
}

func parseHookProxyUpstream(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("parse integration hook upstream: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return nil, fmt.Errorf("integration hook upstream must be an absolute HTTP URL without credentials or fragment")
	}
	return parsed, nil
}

func newHookProxyControlToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate integration hook proxy control token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func joinHookProxyPath(base, request *url.URL) (string, string) {
	baseSlash := strings.HasSuffix(base.Path, "/")
	requestSlash := strings.HasPrefix(request.Path, "/")
	path := base.Path
	if baseSlash && requestSlash {
		path += request.Path[1:]
	} else if !baseSlash && !requestSlash {
		path += "/" + request.Path
	} else {
		path += request.Path
	}

	baseEscaped := base.EscapedPath()
	requestEscaped := request.EscapedPath()
	if baseEscaped == base.Path && requestEscaped == request.Path {
		return path, ""
	}
	rawPath := baseEscaped
	if baseSlash && requestSlash {
		rawPath += requestEscaped[1:]
	} else if !baseSlash && !requestSlash {
		rawPath += "/" + requestEscaped
	} else {
		rawPath += requestEscaped
	}
	return path, rawPath
}

func removeHopHeaders(headers http.Header) {
	for _, name := range headers.Values("Connection") {
		for _, token := range strings.Split(name, ",") {
			headers.Del(strings.TrimSpace(token))
		}
	}
	for _, name := range []string{
		"Connection",
		"Proxy-Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		headers.Del(name)
	}
}

func copyHookProxyHeaders(destination, source http.Header) {
	for name, values := range source {
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}
