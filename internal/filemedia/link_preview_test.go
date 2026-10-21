package filemedia

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestParseLinkPreviewPrefersOpenGraphAndKeepsOnlyHTTPSImages(t *testing.T) {
	t.Parallel()

	base, _ := url.Parse("https://www.discogs.com/release/1-Example")
	preview := parseLinkPreview(base, strings.NewReader(`<!doctype html><html><head>
		<title>Fallback title</title>
		<meta property="og:title" content="  Example &amp; Friends
			– Live  ">
		<meta name="description" content="Plain description">
		<meta name="twitter:description" content="Card description">
		<meta name="application-name" content="Discogs">
		<meta property="og:image" content="http://img.discogs.com/insecure.jpg">
		<meta name="twitter:image" content="/images/cover.jpg">
		</head><body><meta property="og:title" content="Body title"></body></html>`))

	want := linkPreview{
		title:       "Example & Friends – Live",
		description: "Card description",
		siteName:    "Discogs",
		imageSource: "https://www.discogs.com/images/cover.jpg",
	}
	if preview != want {
		t.Fatalf("preview = %+v, want %+v", preview, want)
	}
}

func TestGetLinkPreviewReadsPagesAndImagesThroughTheImportGuard(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != linkPreviewUserAgent {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		switch r.URL.Path {
		case "/watch":
			http.Redirect(w, r, "https://www.example.com/video", http.StatusFound)
		case "/video":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<head><meta property="og:title" content="A video"><meta property="og:site_name" content="Video Site"><meta property="og:image" content="https://i.example.com/v.jpg"></head>`))
		case "/cover.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	service := linkPreviewTestService(server, []net.IP{net.ParseIP("8.8.8.8")}, nil)

	page, err := service.GetLinkPreview(t.Context(), connect.NewRequest(&managev1.GetLinkPreviewRequest{Url: "https://www.example.com/watch"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Msg; got.GetResolvedUrl() != "https://www.example.com/video" || got.GetTitle() != "A video" || got.GetSiteName() != "Video Site" || got.GetImageSource() != "https://i.example.com/v.jpg" {
		t.Fatalf("page preview = %+v", got)
	}
	image, err := service.GetLinkPreview(t.Context(), connect.NewRequest(&managev1.GetLinkPreviewRequest{Url: "https://www.example.com/cover.png"}))
	if err != nil {
		t.Fatal(err)
	}
	if image.Msg.GetImageSource() != "https://www.example.com/cover.png" || image.Msg.GetTitle() != "" {
		t.Fatalf("image preview = %+v", image.Msg)
	}
	if _, err := service.GetLinkPreview(t.Context(), connect.NewRequest(&managev1.GetLinkPreviewRequest{Url: "https://www.example.com/missing"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("missing page error = %v", err)
	}
}

func TestGetLinkPreviewRejectsNonAuthorsAndInternalAddressesBeforeFetching(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the guard let a rejected request reach the network")
	}))
	t.Cleanup(server.Close)
	request := connect.NewRequest(&managev1.GetLinkPreviewRequest{Url: "https://intranet.example.com/"})

	denied := linkPreviewTestService(server, []net.IP{net.ParseIP("8.8.8.8")}, errs.AuthorRequired())
	if _, err := denied.GetLinkPreview(t.Context(), request); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("non-author error = %v", err)
	}
	internal := linkPreviewTestService(server, []net.IP{net.ParseIP("10.0.0.8")}, nil)
	if _, err := internal.GetLinkPreview(t.Context(), request); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("internal address error = %v", err)
	}
	plain := connect.NewRequest(&managev1.GetLinkPreviewRequest{Url: "http://www.example.com/"})
	if _, err := internal.GetLinkPreview(t.Context(), plain); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("plain HTTP error = %v", err)
	}
}

func linkPreviewTestService(server *httptest.Server, ips []net.IP, authorizeErr error) *LinkPreviewService {
	address := strings.TrimPrefix(server.URL, "https://")
	return &LinkPreviewService{
		authorize: func(context.Context) error { return authorizeErr },
		resolver:  &remoteImportTestResolver{ips: ips},
		dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			if address == "" {
				return nil, errors.New("no fixture")
			}
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
		baseTransport: server.Client().Transport.(*http.Transport),
	}
}
