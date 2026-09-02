package filemedia

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	remoteImportSniffBytes   = 64 * 1024
	remoteImportHTTPTimeout  = 10 * time.Minute
	remoteImportMaxRedirects = 5
)

type remoteImportResolver interface {
	LookupIP(context.Context, string, string) ([]net.IP, error)
}

type remoteImportDialer func(context.Context, string, string) (net.Conn, error)

type remoteImportHTTPClient struct {
	*http.Client
	transport *http.Transport
}

func (c *remoteImportHTTPClient) CloseIdleConnections() {
	if c == nil || c.transport == nil {
		return
	}
	c.transport.CloseIdleConnections()
}

type remoteImportTarget struct {
	host string
	ip   net.IP
	port string
}

type remoteImportTargetContextKey struct{}

var remoteImportNonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	// Reject transition prefixes whose apparent public address can encode another target.
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

type remoteFileImportOptions struct {
	uploadType            managev1.UploadType
	entityID              string
	entityType            string
	transcodeEntityType   managev1.TranscodeEntityType
	sourceURL             string
	correlationID         string
	slotID                string
	expectedCurrentFileID *string
	emitLifecycle         bool
	triggerTranscoding    bool
	checkPermission       bool
	operationIdentity     *remoteImportOperationIdentity
	operationLockHeld     bool
}

type remoteFileImportResult struct {
	fileID                string
	fileName              string
	mimeType              string
	size                  int64
	inline                *commonv1.ExpiringMediaRef
	download              *commonv1.ExpiringMediaRef
	slotID                string
	attemptID             string
	expectedCurrentFileID *string
	asset                 *commonv1.AssetRef
}

type countingReader struct {
	reader    io.Reader
	count     int64
	afterRead func(total int64)
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.count += int64(n)
	if n > 0 && r.afterRead != nil {
		r.afterRead(r.count)
	}
	return n, err
}

func readRemoteImportPrefix(reader io.Reader, limit int64) ([]byte, *io.LimitedReader, error) {
	if limit <= 0 {
		return nil, &io.LimitedReader{R: reader, N: 0}, nil
	}

	limited := &io.LimitedReader{R: reader, N: limit}
	prefixSize := remoteImportSniffBytes
	if int64(prefixSize) > limit {
		prefixSize = int(limit)
	}
	if prefixSize <= 0 {
		return nil, limited, nil
	}

	prefix := make([]byte, prefixSize)
	n, err := io.ReadFull(limited, prefix)
	switch err {
	case nil:
		return prefix[:n], limited, nil
	case io.EOF, io.ErrUnexpectedEOF:
		return prefix[:n], limited, nil
	default:
		return nil, nil, err
	}
}

func remoteImportFileName(parsedURL *url.URL) string {
	if parsedURL == nil {
		return ""
	}

	lastSegment := path.Base(parsedURL.Path)
	if lastSegment == "." || lastSegment == "/" || lastSegment == "" {
		return parsedURL.Hostname()
	}

	if decoded, err := url.PathUnescape(lastSegment); err == nil && strings.TrimSpace(decoded) != "" {
		return decoded
	}

	return lastSegment
}

// isInternalHost checks if a hostname is internal (SSRF protection)
func isInternalHost(hostname string) bool {
	if hostname == "localhost" || slices.Contains([]string{
		"metadata.google.internal",
		"metadata.goog",
		"instance-data",
	}, hostname) {
		return true
	}
	ip := net.ParseIP(hostname)
	if ip == nil {
		return false
	}
	return isInternalIP(ip)
}

// isInternalIP rejects addresses that are not globally public routing targets.
func isInternalIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() {
		return true
	}
	for _, prefix := range remoteImportNonPublicPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}

	return false
}

func validateRemoteImportURL(ctx context.Context, resolver remoteImportResolver, parsedURL *url.URL) (remoteImportTarget, error) {
	if parsedURL == nil || parsedURL.Scheme != "https" {
		return remoteImportTarget{}, fmt.Errorf("only HTTPS URLs allowed")
	}
	if parsedURL.User != nil {
		return remoteImportTarget{}, fmt.Errorf("URL credentials not allowed")
	}
	if parsedURL.Port() != "" && parsedURL.Port() != "443" {
		return remoteImportTarget{}, fmt.Errorf("custom ports not allowed")
	}

	hostname := strings.ToLower(parsedURL.Hostname())
	if hostname == "" {
		return remoteImportTarget{}, fmt.Errorf("hostname required")
	}
	if isInternalHost(hostname) {
		return remoteImportTarget{}, fmt.Errorf("internal URLs not allowed")
	}

	ips, err := resolver.LookupIP(ctx, "ip", hostname)
	if err != nil || len(ips) == 0 {
		return remoteImportTarget{}, fmt.Errorf("failed to resolve hostname")
	}
	if slices.ContainsFunc(ips, isInternalIP) {
		return remoteImportTarget{}, fmt.Errorf("internal URLs not allowed")
	}

	return remoteImportTarget{host: hostname, ip: ips[0], port: "443"}, nil
}

func newRemoteImportHTTPClient(
	ctx context.Context,
	resolver remoteImportResolver,
	dial remoteImportDialer,
	baseTransports ...*http.Transport,
) *remoteImportHTTPClient {
	baseTransport := http.DefaultTransport.(*http.Transport)
	if len(baseTransports) > 0 && baseTransports[0] != nil {
		baseTransport = baseTransports[0]
	}
	transport := baseTransport.Clone()
	transport.Proxy = nil
	transport.DialContext = func(dialCtx context.Context, network, _ string) (net.Conn, error) {
		target, ok := dialCtx.Value(remoteImportTargetContextKey{}).(remoteImportTarget)
		if !ok {
			return nil, fmt.Errorf("remote import target was not validated")
		}
		return dial(dialCtx, network, net.JoinHostPort(target.ip.String(), target.port))
	}

	return &remoteImportHTTPClient{Client: &http.Client{
		Timeout:   remoteImportHTTPTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= remoteImportMaxRedirects {
				return fmt.Errorf("too many redirects")
			}
			target, err := validateRemoteImportURL(ctx, resolver, req.URL)
			if err != nil {
				return fmt.Errorf("invalid redirect URL: %w", err)
			}
			*req = *req.WithContext(context.WithValue(req.Context(), remoteImportTargetContextKey{}, target))
			return nil
		},
	}, transport: transport}
}

// DownloadFromUrl downloads a file from an external URL and stores it.
func (s *FileService) DownloadFromUrl(
	ctx context.Context,
	req *connect.Request[managev1.DownloadFromUrlRequest],
) (*connect.Response[managev1.DownloadFromUrlResponse], error) {
	var entityType string
	transcodeEntityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED
	if req.Msg.EntityType != nil {
		entityType = req.Msg.EntityType.String()
		transcodeEntityType = req.Msg.GetEntityType()
	}
	result, err := s.importRemoteFile(ctx, remoteFileImportOptions{
		uploadType:            req.Msg.UploadType,
		entityID:              req.Msg.EntityId,
		entityType:            entityType,
		transcodeEntityType:   transcodeEntityType,
		sourceURL:             req.Msg.Url,
		correlationID:         req.Msg.GetCorrelationId(),
		slotID:                req.Msg.GetSlotId(),
		expectedCurrentFileID: req.Msg.ExpectedCurrentFileId,
		emitLifecycle:         true,
		triggerTranscoding:    true,
		checkPermission:       true,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.DownloadFromUrlResponse{
		FileId:                result.fileID,
		Delivery:              mediaDeliveryFromRemoteImport(result),
		SlotId:                optionalNonEmptyString(result.slotID),
		IngestAttemptId:       optionalNonEmptyString(result.attemptID),
		ExpectedCurrentFileId: result.expectedCurrentFileID,
	}), nil
}

func mediaDeliveryFromRemoteImport(result *remoteFileImportResult) *commonv1.MediaDelivery {
	if result == nil {
		return nil
	}
	fileName := result.fileName
	return &commonv1.MediaDelivery{
		FileId:    result.fileID,
		Extension: mediaExtension(&result.mimeType),
		MimeType:  result.mimeType,
		FileSize:  result.size,
		FileName:  &fileName,
		Inline:    result.inline,
		Download:  result.download,
		Asset:     result.asset,
	}
}
