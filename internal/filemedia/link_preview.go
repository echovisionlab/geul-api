package filemedia

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const (
	linkPreviewTimeout       = 8 * time.Second
	linkPreviewMaxBytes      = 1 << 20
	linkPreviewTitleRunes    = 300
	linkPreviewSiteRunes     = 120
	linkPreviewDescribeRunes = 1000
	linkPreviewUserAgent     = "Mozilla/5.0 (compatible; GeulLinkPreview/1.0)"
)

// LinkPreviewService reads the preview metadata and site icon a page publishes about itself.
// It fetches the page directly through the remote File import guard (HTTPS
// only, public addresses, re-validated redirects) and stores nothing.
type LinkPreviewService struct {
	authorize     func(context.Context) error
	resolver      remoteImportResolver
	dial          remoteImportDialer
	baseTransport *http.Transport
}

func NewLinkPreviewService(spiceDB *auth.SpiceDBClient) *LinkPreviewService {
	if spiceDB == nil {
		panic("spiceDB is required")
	}
	return &LinkPreviewService{
		authorize: func(ctx context.Context) error { return requirePlatformAuthor(ctx, spiceDB) },
		resolver:  net.DefaultResolver,
		dial:      new(net.Dialer).DialContext,
	}
}

func requirePlatformAuthor(ctx context.Context, spiceDB *auth.SpiceDBClient) error {
	user := auth.GetUser(ctx)
	if user == nil || user.IdentityID.String() == "" {
		return errs.AuthenticationRequired()
	}
	can, err := policyv1.Platform.IsAuthor()
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	authorizationCtx := auth.WithUser(ctx, user)
	decision, err := auth.AuthorizationDecision(authorizationCtx, can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := spiceDB.Can(authorizationCtx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.AuthorRequired()
	}
	return nil
}

// GetLinkPreviewQuery reads the preview of one HTTPS page.
type GetLinkPreviewQuery struct {
	URL string
}

func (s *LinkPreviewService) GetLinkPreview(ctx context.Context, query GetLinkPreviewQuery) (*managev1.GetLinkPreviewResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	parsedURL, err := url.Parse(strings.TrimSpace(query.URL))
	if err != nil {
		return nil, errs.InvalidArgument("url", "invalid URL")
	}
	ctx, cancel := context.WithTimeout(ctx, linkPreviewTimeout)
	defer cancel()
	target, err := validateRemoteImportURL(ctx, s.resolver, parsedURL)
	if err != nil {
		return nil, errs.InvalidArgument("url", err.Error())
	}
	client := newRemoteImportHTTPClient(ctx, s.resolver, s.dial, s.baseTransport)
	defer client.CloseIdleConnections()
	httpRequest, err := http.NewRequestWithContext(
		context.WithValue(ctx, remoteImportTargetContextKey{}, target),
		http.MethodGet,
		parsedURL.String(),
		nil,
	)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("create link preview request: %w", err))
	}
	httpRequest.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.1")
	httpRequest.Header.Set("User-Agent", linkPreviewUserAgent)
	response, err := client.Do(httpRequest)
	if err != nil {
		return nil, errs.WithCause(errs.FailedPrecondition("the linked page could not be read"), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errs.FailedPrecondition(fmt.Sprintf("the linked page returned status %d", response.StatusCode))
	}

	resolved := response.Request.URL
	result := &managev1.GetLinkPreviewResponse{ResolvedUrl: resolved.String()}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	switch {
	case strings.HasPrefix(mediaType, "image/"):
		// A direct image link previews as itself.
		result.ImageSource = resolved.String()
		result.IconSource = siteIconSource(resolved, nil)
	case mediaType == "text/html" || mediaType == "application/xhtml+xml" || mediaType == "":
		body, err := charset.NewReader(io.LimitReader(response.Body, linkPreviewMaxBytes), response.Header.Get("Content-Type"))
		if err != nil {
			return result, nil
		}
		preview := parseLinkPreview(resolved, body)
		result.Title, result.Description, result.SiteName, result.ImageSource, result.IconSource =
			preview.title, preview.description, preview.siteName, preview.imageSource, preview.iconSource
	}
	return result, nil
}

type linkPreview struct {
	title, description, siteName, imageSource, iconSource string
}

// parseLinkPreview reads the document head. Open Graph wins over Twitter
// card metadata, which wins over the plain title and description.
func parseLinkPreview(base *url.URL, body io.Reader) linkPreview {
	meta := map[string]string{}
	icons := map[string]string{}
	var documentTitle string
	tokenizer := html.NewTokenizer(body)
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return buildLinkPreview(base, meta, icons, documentTitle)
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttributes := tokenizer.TagName()
			switch string(name) {
			case "body":
				return buildLinkPreview(base, meta, icons, documentTitle)
			case "link":
				attributes := linkPreviewTagAttributes(tokenizer, hasAttributes, "rel", "href")
				rel := strings.Join(strings.Fields(strings.ToLower(attributes["rel"])), " ")
				keepFirstLinkPreviewValue(icons, rel, attributes["href"])
			case "title":
				if documentTitle == "" && tokenizer.Next() == html.TextToken {
					documentTitle = string(tokenizer.Text())
				}
			case "meta":
				attributes := linkPreviewTagAttributes(tokenizer, hasAttributes, "property", "name", "content")
				keepFirstLinkPreviewValue(meta, strings.ToLower(strings.TrimSpace(attributes["key"])), attributes["content"])
			}
		}
	}
}

// linkPreviewTagAttributes reads the named attributes of the current tag.
// "property" and "name" both fill "key", and the last one present wins.
func linkPreviewTagAttributes(tokenizer *html.Tokenizer, hasAttributes bool, names ...string) map[string]string {
	attributes := map[string]string{}
	for hasAttributes {
		var attribute, value []byte
		attribute, value, hasAttributes = tokenizer.TagAttr()
		name := string(attribute)
		if !slices.Contains(names, name) {
			continue
		}
		if name == "property" || name == "name" {
			name = "key"
		}
		attributes[name] = string(value)
	}
	return attributes
}

func keepFirstLinkPreviewValue(values map[string]string, key, value string) {
	if _, seen := values[key]; key != "" && !seen {
		values[key] = value
	}
}

func buildLinkPreview(base *url.URL, meta, icons map[string]string, documentTitle string) linkPreview {
	first := func(limit int, values ...string) string {
		for _, value := range values {
			if text := clampRunes(strings.Join(strings.Fields(value), " "), limit); text != "" {
				return text
			}
		}
		return ""
	}
	preview := linkPreview{
		title:       first(linkPreviewTitleRunes, meta["og:title"], meta["twitter:title"], documentTitle),
		description: first(linkPreviewDescribeRunes, meta["og:description"], meta["twitter:description"], meta["description"]),
		siteName:    first(linkPreviewSiteRunes, meta["og:site_name"], meta["application-name"]),
	}
	for _, key := range []string{"og:image:secure_url", "og:image", "og:image:url", "twitter:image", "twitter:image:src"} {
		if source := httpsReference(base, meta[key]); source != "" {
			preview.imageSource = source
			break
		}
	}
	preview.iconSource = siteIconSource(base, icons)
	return preview
}

// siteIconSource prefers a declared icon, then the touch icon, then the
// conventional /favicon.ico; the editor's import rejects anything not an image.
func siteIconSource(base *url.URL, icons map[string]string) string {
	for _, rel := range []string{"icon", "shortcut icon", "apple-touch-icon", "apple-touch-icon-precomposed"} {
		if source := httpsReference(base, icons[rel]); source != "" {
			return source
		}
	}
	return httpsReference(base, "/favicon.ico")
}

// httpsReference resolves a declared image against the page and keeps it only
// when the result is HTTPS; the editor imports it through the same guard.
func httpsReference(base *url.URL, value string) string {
	reference, err := url.Parse(strings.TrimSpace(value))
	if err != nil || strings.TrimSpace(value) == "" {
		return ""
	}
	resolved := base.ResolveReference(reference)
	if resolved.Scheme != "https" || resolved.Host == "" || resolved.User != nil {
		return ""
	}
	return resolved.String()
}

func clampRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}
