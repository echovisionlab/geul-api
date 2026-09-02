package filemedia

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/postgreslock"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type remoteImportTestResolver struct {
	ips   []net.IP
	err   error
	calls int
}

func (r *remoteImportTestResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	r.calls++
	return r.ips, r.err
}

func TestMediaDeliveryFromRemoteImportPreservesPurposeSpecificRefs(t *testing.T) {
	t.Parallel()

	inline := &commonv1.ExpiringMediaRef{Url: "https://cdn.example.com/inline"}
	download := &commonv1.ExpiringMediaRef{Url: "https://cdn.example.com/download"}
	delivery := mediaDeliveryFromRemoteImport(&remoteFileImportResult{
		fileID:   "file-1",
		fileName: "image.webp",
		mimeType: "image/webp",
		size:     128,
		inline:   inline,
		download: download,
	})

	if delivery.GetInline() != inline || delivery.GetDownload() != download {
		t.Fatalf("purpose-specific refs were not preserved: %#v", delivery)
	}
}

func TestResolveRemoteImportOperationIdentityRequiresDurableCorrelation(t *testing.T) {
	t.Parallel()

	_, err := resolveRemoteImportOperationIdentity(
		remoteFileImportOptions{
			uploadType:          managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
			transcodeEntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE,
		},
		"",
		"",
		fileIngestProjectionIdentity{mode: fileIngestTargetModeEditorFile},
	)
	if err == nil || !strings.Contains(err.Error(), "correlation_id") {
		t.Fatalf("missing durable correlation error = %v", err)
	}

	_, err = resolveRemoteImportOperationIdentity(
		remoteFileImportOptions{
			uploadType:          managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
			transcodeEntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
			correlationID:       "not-a-uuid",
		},
		uuid.NewString(),
		"",
		fileIngestProjectionIdentity{mode: fileIngestTargetModeTrackProjection},
	)
	if err == nil || !strings.Contains(err.Error(), "valid non-empty UUID") {
		t.Fatalf("invalid durable correlation error = %v", err)
	}
}

func TestResolveRemoteImportOperationIdentityIsDurableForDedicatedPublicAsset(t *testing.T) {
	t.Parallel()

	correlationID := uuid.NewString()
	entityID := uuid.NewString()
	opts := remoteFileImportOptions{
		uploadType:    managev1.UploadType_UPLOAD_TYPE_CLIENT_LOGO,
		correlationID: correlationID,
	}
	first, err := resolveRemoteImportOperationIdentity(opts, entityID, "", fileIngestProjectionIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveRemoteImportOperationIdentity(opts, entityID, "", fileIngestProjectionIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if !first.durable || first.fileID != second.fileID || first.attemptID != correlationID {
		t.Fatalf("dedicated public asset identity was not stable: first=%+v second=%+v", first, second)
	}
}

func TestResolveRemoteImportOperationIdentityIsDeterministicForIndependentFile(t *testing.T) {
	t.Parallel()

	correlationID := uuid.NewString()
	opts := remoteFileImportOptions{
		uploadType:          managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		transcodeEntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE,
		correlationID:       strings.ToUpper(correlationID),
		sourceURL:           "https://first.example/audio.wav",
	}
	projection := fileIngestProjectionIdentity{
		mode: fileIngestTargetModeEditorFile,
	}

	first, err := resolveRemoteImportOperationIdentity(opts, "", "", projection)
	if err != nil {
		t.Fatal(err)
	}
	opts.correlationID = correlationID
	opts.sourceURL = "https://retry.example/replacement.wav"
	second, err := resolveRemoteImportOperationIdentity(opts, "", "", projection)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("same durable operation produced different identities: %#v != %#v", first, second)
	}
	if first.attemptID != correlationID || !first.durable {
		t.Fatalf("unexpected stable attempt identity: %#v", first)
	}
	if _, err := uuid.Parse(first.fileID); err != nil {
		t.Fatalf("deterministic file ID is not a UUID: %v", err)
	}

	opts.correlationID = uuid.NewString()
	changedCorrelation, err := resolveRemoteImportOperationIdentity(opts, "", "", projection)
	if err != nil {
		t.Fatal(err)
	}
	if changedCorrelation.fileID == first.fileID {
		t.Fatal("different durable correlation reused deterministic file ID")
	}
}

func TestWithRemoteImportAdvisoryLockNoOpsForTestDialect(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open("file:remote-import-lock-"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = withRemoteImportAdvisoryLock(context.Background(), db, uuid.NewString(), func(locked *gorm.DB) error {
		called = true
		if locked == nil {
			t.Fatal("test dialect lock callback received nil database")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("test dialect lock callback was not called")
	}
}

func TestAcquireBoundedAdvisoryLockSlotIsBoundedAndContextAware(t *testing.T) {
	t.Parallel()

	slots := make(chan struct{}, 1)
	release, err := postgreslock.AcquireSlot(context.Background(), slots)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 1 {
		t.Fatalf("slot count = %d, want 1", len(slots))
	}

	waitCtx, cancel := context.WithCancel(context.Background())
	cancel()
	blockedRelease, err := postgreslock.AcquireSlot(waitCtx, slots)
	if !errors.Is(err, context.Canceled) || blockedRelease != nil {
		t.Fatalf("blocked acquire returned release=%t err=%v, want context cancellation", blockedRelease != nil, err)
	}

	release()
	if len(slots) != 0 {
		t.Fatalf("slot count after release = %d, want 0", len(slots))
	}
	release, err = postgreslock.AcquireSlot(context.Background(), slots)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestReadRemoteImportPrefixReadsBoundedPrefix(t *testing.T) {
	t.Parallel()

	prefix, limited, err := readRemoteImportPrefix(strings.NewReader("abcdefghij"), 6)
	if err != nil {
		t.Fatalf("readRemoteImportPrefix returned error: %v", err)
	}

	if got := string(prefix); got != "abcdef" {
		t.Fatalf("unexpected prefix: %q", got)
	}

	rest, err := io.ReadAll(limited)
	if err != nil {
		t.Fatalf("failed to read remaining bytes: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("expected no remaining bytes within bounded limit, got %q", string(rest))
	}
}

func TestReadRemoteImportPrefixPreservesRemainingBody(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("a", remoteImportSniffBytes+3)
	prefix, limited, err := readRemoteImportPrefix(strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("readRemoteImportPrefix returned error: %v", err)
	}

	if len(prefix) != remoteImportSniffBytes {
		t.Fatalf("unexpected prefix length: %d", len(prefix))
	}

	rest, err := io.ReadAll(limited)
	if err != nil {
		t.Fatalf("failed to read remaining bytes: %v", err)
	}
	if len(rest) != 3 {
		t.Fatalf("expected 3 trailing bytes after prefix, got %d", len(rest))
	}
}

func TestReadRemoteImportPrefixRejectsNonPositiveLimit(t *testing.T) {
	t.Parallel()

	prefix, limited, err := readRemoteImportPrefix(strings.NewReader("abcdef"), 0)
	if err != nil {
		t.Fatalf("readRemoteImportPrefix returned error: %v", err)
	}
	if len(prefix) != 0 {
		t.Fatalf("expected empty prefix, got %q", string(prefix))
	}
	if limited == nil || limited.N != 0 {
		t.Fatalf("expected zero-byte limited reader, got %#v", limited)
	}
}

func TestRemoteImportFileName(t *testing.T) {
	t.Parallel()

	parsed, err := url.Parse("https://example.com/media/My%20Audio%20Track.mp3?download=1")
	if err != nil {
		t.Fatalf("failed to parse URL: %v", err)
	}

	if got := remoteImportFileName(parsed); got != "My Audio Track.mp3" {
		t.Fatalf("unexpected remote file name: %q", got)
	}

	rootURL, err := url.Parse("https://example.com/")
	if err != nil {
		t.Fatalf("failed to parse root URL: %v", err)
	}

	if got := remoteImportFileName(rootURL); got != "example.com" {
		t.Fatalf("expected hostname fallback, got %q", got)
	}

	if got := remoteImportFileName(nil); got != "" {
		t.Fatalf("expected empty name for nil URL, got %q", got)
	}

	invalidEscapedURL := &url.URL{Scheme: "https", Host: "example.com", Path: "/media/bad%zzname.mp3"}

	if got := remoteImportFileName(invalidEscapedURL); got != "bad%zzname.mp3" {
		t.Fatalf("expected undecoded path segment fallback, got %q", got)
	}
}

func TestInternalHostProtection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		host string
		want bool
	}{
		{name: "localhost", host: "localhost", want: true},
		{name: "loopback ipv4", host: "127.0.0.1", want: true},
		{name: "metadata", host: "metadata.google.internal", want: true},
		{name: "private ten", host: "10.0.4.5", want: true},
		{name: "private one seven two", host: "172.20.4.5", want: true},
		{name: "private one nine two", host: "192.168.4.5", want: true},
		{name: "link local", host: "169.254.169.254", want: true},
		{name: "public", host: "storage.example.com", want: false},
		{name: "public ip", host: "8.8.8.8", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isInternalHost(tt.host); got != tt.want {
				t.Fatalf("isInternalHost(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestInternalIPProtection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ip   net.IP
		want bool
	}{
		{name: "nil", ip: nil, want: true},
		{name: "unspecified ipv4", ip: net.ParseIP("0.0.0.0"), want: true},
		{name: "unspecified ipv6", ip: net.ParseIP("::"), want: true},
		{name: "loopback", ip: net.ParseIP("127.0.0.1"), want: true},
		{name: "private ipv4", ip: net.ParseIP("10.0.0.1"), want: true},
		{name: "link local", ip: net.ParseIP("169.254.10.20"), want: true},
		{name: "carrier grade NAT", ip: net.ParseIP("100.64.1.1"), want: true},
		{name: "protocol assignments", ip: net.ParseIP("192.0.0.8"), want: true},
		{name: "IPv4 documentation", ip: net.ParseIP("203.0.113.10"), want: true},
		{name: "IPv4 benchmarking", ip: net.ParseIP("198.18.0.1"), want: true},
		{name: "IPv4 multicast", ip: net.ParseIP("224.0.0.1"), want: true},
		{name: "IPv4 reserved", ip: net.ParseIP("240.0.0.1"), want: true},
		{name: "unique local ipv6", ip: net.ParseIP("fc00::1"), want: true},
		{name: "IPv6 documentation", ip: net.ParseIP("2001:db8::1"), want: true},
		{name: "IPv6 benchmarking", ip: net.ParseIP("2001:2::1"), want: true},
		{name: "IPv6 multicast", ip: net.ParseIP("ff02::1"), want: true},
		{name: "IPv6 discarded prefix", ip: net.ParseIP("100::1"), want: true},
		{name: "IPv6 NAT64 embedded private", ip: net.ParseIP("64:ff9b::a00:1"), want: true},
		{name: "IPv6 6to4 embedded private", ip: net.ParseIP("2002:0a00:0001::1"), want: true},
		{name: "public ipv4", ip: net.ParseIP("8.8.8.8"), want: false},
		{name: "public ipv6", ip: net.ParseIP("2001:4860:4860::8888"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isInternalIP(tt.ip); got != tt.want {
				t.Fatalf("isInternalIP(%v) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestValidateRemoteImportURLPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rawURL  string
		ips     []net.IP
		wantErr string
	}{
		{name: "public HTTPS", rawURL: "https://media.example.com/video.mp4", ips: []net.IP{net.ParseIP("8.8.8.8")}},
		{name: "explicit default port", rawURL: "https://media.example.com:443/video.mp4", ips: []net.IP{net.ParseIP("8.8.8.8")}},
		{name: "HTTP", rawURL: "http://media.example.com/video.mp4", ips: []net.IP{net.ParseIP("8.8.8.8")}, wantErr: "only HTTPS"},
		{name: "credentials", rawURL: "https://user:secret@media.example.com/video.mp4", ips: []net.IP{net.ParseIP("8.8.8.8")}, wantErr: "credentials"},
		{name: "custom port", rawURL: "https://media.example.com:8443/video.mp4", ips: []net.IP{net.ParseIP("8.8.8.8")}, wantErr: "custom ports"},
		{name: "private address", rawURL: "https://media.example.com/video.mp4", ips: []net.IP{net.ParseIP("10.0.0.8")}, wantErr: "internal URLs"},
		{name: "documentation address", rawURL: "https://media.example.com/video.mp4", ips: []net.IP{net.ParseIP("203.0.113.10")}, wantErr: "internal URLs"},
		{name: "mixed public and private addresses", rawURL: "https://media.example.com/video.mp4", ips: []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("127.0.0.1")}, wantErr: "internal URLs"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parsed, err := url.Parse(tt.rawURL)
			if err != nil {
				t.Fatal(err)
			}
			resolver := &remoteImportTestResolver{ips: tt.ips}
			target, err := validateRemoteImportURL(context.Background(), resolver, parsed)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("validateRemoteImportURL() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateRemoteImportURL() error = %v", err)
			}
			if target.host != "media.example.com" || !target.ip.Equal(tt.ips[0]) || target.port != "443" {
				t.Fatalf("unexpected target: %#v", target)
			}
		})
	}
}

func TestRemoteImportRedirectPolicyRevalidatesEveryHop(t *testing.T) {
	t.Parallel()

	resolver := &remoteImportTestResolver{ips: []net.IP{net.ParseIP("10.0.0.8")}}
	client := newRemoteImportHTTPClient(context.Background(), resolver, func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("not used")
	})
	req, err := http.NewRequest(http.MethodGet, "https://redirect.example.com/video.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(req, []*http.Request{{}, {}}); err == nil || !strings.Contains(err.Error(), "internal URLs") {
		t.Fatalf("private redirect error = %v", err)
	}
	if resolver.calls != 1 {
		t.Fatalf("redirect hostname resolved %d times, want 1", resolver.calls)
	}

	resolver.ips = []net.IP{net.ParseIP("1.1.1.1")}
	if err := client.CheckRedirect(req, make([]*http.Request, remoteImportMaxRedirects)); err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Fatalf("redirect limit error = %v", err)
	}
}

func TestRemoteImportDialUsesValidatedIP(t *testing.T) {
	t.Parallel()

	resolver := &remoteImportTestResolver{ips: []net.IP{net.ParseIP("8.8.8.8")}}
	parsed, err := url.Parse("https://media.example.com/video.mp4")
	if err != nil {
		t.Fatal(err)
	}
	target, err := validateRemoteImportURL(context.Background(), resolver, parsed)
	if err != nil {
		t.Fatal(err)
	}

	var dialAddress string
	client := newRemoteImportHTTPClient(context.Background(), resolver, func(_ context.Context, _, address string) (net.Conn, error) {
		dialAddress = address
		return nil, errors.New("dial stopped by test")
	})
	transport := client.Transport.(*http.Transport)
	dialContext := context.WithValue(context.Background(), remoteImportTargetContextKey{}, target)
	_, _ = transport.DialContext(dialContext, "tcp", "media.example.com:443")

	resolver.ips = []net.IP{net.ParseIP("127.0.0.1")}
	if dialAddress != "8.8.8.8:443" {
		t.Fatalf("dial address = %q, want validated public IP", dialAddress)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver called again during dial: %d calls", resolver.calls)
	}
}

func TestRemoteImportTransportIgnoresHTTPSProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:3128")

	var dialAddress string
	client := newRemoteImportHTTPClient(context.Background(), &remoteImportTestResolver{}, func(_ context.Context, _, address string) (net.Conn, error) {
		dialAddress = address
		return nil, errors.New("dial stopped by test")
	})
	transport := client.Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Fatal("remote import transport must not use environment proxies")
	}

	target := remoteImportTarget{host: "media.example.com", ip: net.ParseIP("8.8.8.8"), port: "443"}
	dialContext := context.WithValue(context.Background(), remoteImportTargetContextKey{}, target)
	_, _ = transport.DialContext(dialContext, "tcp", "127.0.0.1:3128")
	if dialAddress != "8.8.8.8:443" {
		t.Fatalf("dial address = %q, want pinned target despite HTTPS_PROXY", dialAddress)
	}
}

func TestRemoteImportHTTPClientOwnsClonedTransport(t *testing.T) {
	t.Parallel()

	client := newRemoteImportHTTPClient(context.Background(), &remoteImportTestResolver{}, func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("dial is not expected")
	})
	if client.transport == nil {
		t.Fatal("request-owned transport is nil")
	}
	if client.Transport != client.transport {
		t.Fatal("client does not use its owned transport")
	}
	if client.transport == http.DefaultTransport {
		t.Fatal("remote import client unexpectedly owns the shared default transport")
	}

	// Closing the request-owned clone must be safe without mutating the shared
	// process transport.
	client.CloseIdleConnections()
}

func TestRemoteNormalizationHTTPClientOwnsProxyFreeTransport(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:3128")

	client := newRemoteNormalizationHTTPClient()
	if client.transport == nil {
		t.Fatal("remote normalization transport is nil")
	}
	if client.Transport != client.transport {
		t.Fatal("remote normalization client does not use its owned transport")
	}
	if client.transport == http.DefaultTransport {
		t.Fatal("remote normalization client unexpectedly owns the shared default transport")
	}
	if client.transport.Proxy != nil {
		t.Fatal("remote normalization transport must not use environment proxies")
	}

	client.CloseIdleConnections()
}

func TestFormatAllowedExtensionsDedupesAliases(t *testing.T) {
	t.Parallel()

	got := formatAllowedExtensions([]string{
		"application/zip",
		"application/x-zip-compressed",
		"application/json",
	})

	if got != "ZIP, JSON" {
		t.Fatalf("unexpected allowed extensions: %q", got)
	}
}

func TestFormatAllowedExtensionsDedupesAIFFAliases(t *testing.T) {
	t.Parallel()

	got := formatAllowedExtensions([]string{
		"audio/aiff",
		"audio/x-aiff",
		"audio/mp4",
	})

	if got != "AIFF, M4A" {
		t.Fatalf("unexpected allowed extensions: %q", got)
	}
}

func TestUnsupportedMimeMessageIncludesSupportedFormats(t *testing.T) {
	t.Parallel()

	got := unsupportedMimeMessage("image/svg+xml", []string{
		"image/jpeg",
		"image/png",
		"image/webp",
		"image/avif",
	})

	if !strings.Contains(got, "Unsupported file type (image/svg+xml).") {
		t.Fatalf("unexpected unsupported MIME prefix: %q", got)
	}
	if !strings.Contains(got, "Supported formats: JPG, PNG, WEBP, AVIF.") {
		t.Fatalf("missing supported formats guidance: %q", got)
	}
}

func TestShouldNormalizeManagedRemoteImport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		uploadType managev1.UploadType
		mimeType   string
		want       bool
	}{
		{name: "avatar jpeg", uploadType: managev1.UploadType_UPLOAD_TYPE_USER_AVATAR, mimeType: "image/jpeg", want: true},
		{name: "editor avif", uploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE, mimeType: "image/avif", want: true},
		{name: "site og webp", uploadType: managev1.UploadType_UPLOAD_TYPE_SITE_OG_BACKGROUND, mimeType: "image/webp", want: true},
		{name: "editor gif", uploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE, mimeType: "image/gif", want: false},
		{name: "logo png", uploadType: managev1.UploadType_UPLOAD_TYPE_CLIENT_LOGO, mimeType: "image/png", want: false},
		{name: "avatar svg", uploadType: managev1.UploadType_UPLOAD_TYPE_USER_AVATAR, mimeType: "image/svg+xml", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := shouldNormalizeManagedRemoteImport(tt.uploadType, tt.mimeType); got != tt.want {
				t.Fatalf("shouldNormalizeManagedRemoteImport(%v, %q) = %v, want %v", tt.uploadType, tt.mimeType, got, tt.want)
			}
		})
	}
}

func TestGetRemoteImportSelectionMaxSize(t *testing.T) {
	t.Parallel()

	if got := getRemoteImportSelectionMaxSize(managev1.UploadType_UPLOAD_TYPE_USER_AVATAR, 30*1024*1024); got != managedRemoteImportSelectionLimit {
		t.Fatalf("managed raster selection max size = %d, want %d", got, managedRemoteImportSelectionLimit)
	}

	if got := getRemoteImportSelectionMaxSize(managev1.UploadType_UPLOAD_TYPE_CLIENT_LOGO, 10*1024*1024); got != 10*1024*1024 {
		t.Fatalf("non-managed selection max size = %d, want %d", got, 10*1024*1024)
	}
}

func TestManagedRasterUploadLimits(t *testing.T) {
	t.Parallel()

	if managedRemoteImportSelectionLimit != 20*1024*1024 {
		t.Fatalf("managed raster selection limit = %d, want 20MB", managedRemoteImportSelectionLimit)
	}
	if managedRemoteImportFinalMaxSize != 30*1024*1024 {
		t.Fatalf("managed raster final max size = %d, want 30MB", managedRemoteImportFinalMaxSize)
	}
	for _, uploadType := range []managev1.UploadType{
		managev1.UploadType_UPLOAD_TYPE_USER_AVATAR,
		managev1.UploadType_UPLOAD_TYPE_ARTIST_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_WORK_FEATURED_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_SERIES_FEATURED_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_FORM_FEATURED_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_PROGRAM_EVENT_POSTER,
		managev1.UploadType_UPLOAD_TYPE_RELEASE_ARTWORK,
		managev1.UploadType_UPLOAD_TYPE_SITE_OG_BACKGROUND,
	} {
		cfg := model.DefaultUploadConfigs[uploadType]
		if cfg == nil {
			t.Fatalf("missing upload config for %s", uploadType)
		}
		if cfg.MaxSize != model.ManagedRasterFinalMaxSize {
			t.Fatalf("%s max size = %d, want %d", uploadType, cfg.MaxSize, model.ManagedRasterFinalMaxSize)
		}
	}
}

func TestBuildManagedImageVariantURL(t *testing.T) {
	t.Parallel()

	rawURL, err := buildManagedImageVariantURL("https://cdn.example.com/media/user/avatar/file.webp?fit=fill", 4096, 84)
	if err != nil {
		t.Fatalf("buildManagedImageVariantURL returned error: %v", err)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("failed to parse variant URL: %v", err)
	}

	query := parsed.Query()
	if got := query.Get("w"); got != "4096" {
		t.Fatalf("unexpected width query: %q", got)
	}
	if got := query.Get("h"); got != "4096" {
		t.Fatalf("unexpected height query: %q", got)
	}
	if got := query.Get("q"); got != "84" {
		t.Fatalf("unexpected quality query: %q", got)
	}
	if got := query.Get("fit"); got != "fill" {
		t.Fatalf("expected existing query params to be preserved, got fit=%q", got)
	}
}

func TestNormalizeRemoteImportStoredFileName(t *testing.T) {
	t.Parallel()

	if got := normalizeRemoteImportStoredFileName("cover.jpg", "image/webp"); got != "cover.webp" {
		t.Fatalf("expected converted extension, got %q", got)
	}
	if got := normalizeRemoteImportStoredFileName("example.com", "image/webp"); got != "example.webp" {
		t.Fatalf("expected appended extension, got %q", got)
	}
}

func TestNextManagedImageDimensionMonotonic(t *testing.T) {
	t.Parallel()

	dimensions := []int{managedRemoteImportMaxDimension}
	current := managedRemoteImportMaxDimension
	for current > managedRemoteImportMinDimension {
		current = nextManagedImageDimension(current)
		dimensions = append(dimensions, current)
	}

	for i := 1; i < len(dimensions); i++ {
		if dimensions[i-1] < dimensions[i] {
			t.Fatalf("expected dimensions to be monotonically descending, got %v", dimensions)
		}
	}

	if got := dimensions[len(dimensions)-1]; got != managedRemoteImportMinDimension {
		t.Fatalf("expected last dimension to clamp at %d, got %d", managedRemoteImportMinDimension, got)
	}
}
