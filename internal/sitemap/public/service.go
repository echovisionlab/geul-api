package public

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	sitemapdomain "github.com/echovisionlab/geul-api/internal/sitemap"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	sitemapSnapshotKeyPrefix = "public:sitemap:v1"
	sitemapRetryAfterSeconds = 300
	sitemapXMLContentType    = "application/xml; charset=utf-8"
	sitemapTextContentType   = "text/plain; charset=utf-8"
)

type sitemapRenderedDocument struct {
	Content     string
	ContentType string
	GeneratedAt time.Time
}

type sitemapURLRow struct {
	Loc     string
	LastMod *time.Time
}

type sitemapIndexRow struct {
	Loc string
}

type sitemapSiteContext struct {
	CanonicalOrigin string
	HomepagePageID  *string
}

type urlSetDocument struct {
	XMLName xml.Name            `xml:"urlset"`
	Xmlns   string              `xml:"xmlns,attr"`
	URLs    []urlSetDocumentURL `xml:"url"`
}

type urlSetDocumentURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

type sitemapIndexDocument struct {
	XMLName  xml.Name                    `xml:"sitemapindex"`
	Xmlns    string                      `xml:"xmlns,attr"`
	Sitemaps []sitemapIndexDocumentEntry `xml:"sitemap"`
}

type sitemapIndexDocumentEntry struct {
	Loc string `xml:"loc"`
}

type sitemapMetrics struct {
	responseCounter          otelmetric.Int64Counter
	generationFailureCounter otelmetric.Int64Counter
	staleAgeHistogram        otelmetric.Int64Histogram
}

// SitemapService generates crawler-facing sitemap documents and owns stale snapshot policy.
type SitemapService struct {
	openv1connect.UnimplementedSitemapServiceHandler
	store           sitemapdomain.Store
	canonicalOrigin string
	metrics         sitemapMetrics
}

// NewSitemapService creates the crawler-facing Sitemap handler.
func NewSitemapService(
	store sitemapdomain.Store,
	siteOrigin string,
) *SitemapService {
	if store == nil {
		panic("sitemap store is required")
	}
	return &SitemapService{
		store:           store,
		canonicalOrigin: normalizeCanonicalOrigin(siteOrigin),
		metrics:         newSitemapMetrics(),
	}
}

func (s *SitemapService) GetDocument(
	ctx context.Context,
	req *connect.Request[openv1.GetSitemapDocumentRequest],
) (*connect.Response[openv1.GetSitemapDocumentResponse], error) {
	kind := req.Msg.Kind
	if kind == openv1.SitemapDocumentKind_SITEMAP_DOCUMENT_KIND_UNSPECIFIED {
		return nil, errs.Required("kind")
	}

	document, err := s.generateDocument(ctx, kind)
	snapshotKey := makeSitemapSnapshotKey(kind)
	if err == nil {
		snapshot := &sitemapdomain.Snapshot{
			Content:     document.Content,
			ContentType: document.ContentType,
			GeneratedAt: document.GeneratedAt,
		}
		if _, saveErr := s.store.SaveSnapshot(ctx, snapshotKey, snapshot); saveErr != nil {
			slog.Warn("failed to save sitemap snapshot", "kind", kind.String(), "error", saveErr)
		}
		s.recordFreshResponseMetric(ctx, kind)

		return connect.NewResponse(&openv1.GetSitemapDocumentResponse{
			Status:      openv1.SitemapDocumentStatus_SITEMAP_DOCUMENT_STATUS_FRESH,
			Content:     document.Content,
			ContentType: document.ContentType,
			GeneratedAt: timestamppb.New(document.GeneratedAt),
		}), nil
	}

	s.recordGenerationFailureMetric(ctx, kind)

	var connectErr *connect.Error
	if ok := errors.As(err, &connectErr); ok && connectErr.Code() == connect.CodeInvalidArgument {
		return nil, err
	}

	snapshot, loadErr := s.store.LoadSnapshot(ctx, snapshotKey)
	if loadErr != nil {
		slog.Error(
			"failed to load sitemap snapshot",
			"kind",
			kind.String(),
			"error",
			loadErr,
			"generation_error",
			err,
		)
	}

	if snapshot != nil {
		slog.Warn(
			"serving stale sitemap snapshot",
			"kind",
			kind.String(),
			"age_seconds",
			int(time.Since(snapshot.GeneratedAt).Seconds()),
			"generation_error",
			err,
		)
		s.recordStaleResponseMetric(ctx, kind, snapshot.GeneratedAt)

		return connect.NewResponse(&openv1.GetSitemapDocumentResponse{
			Status:      openv1.SitemapDocumentStatus_SITEMAP_DOCUMENT_STATUS_STALE,
			Content:     snapshot.Content,
			ContentType: snapshot.ContentType,
			GeneratedAt: timestamppb.New(snapshot.GeneratedAt),
		}), nil
	}

	slog.Error("sitemap unavailable with no snapshot", "kind", kind.String(), "error", err)
	s.recordUnavailableResponseMetric(ctx, kind)

	return connect.NewResponse(&openv1.GetSitemapDocumentResponse{
		Status:            openv1.SitemapDocumentStatus_SITEMAP_DOCUMENT_STATUS_UNAVAILABLE,
		Content:           "Sitemap unavailable.\n",
		ContentType:       sitemapTextContentType,
		RetryAfterSeconds: &[]int32{sitemapRetryAfterSeconds}[0],
	}), nil
}

func (s *SitemapService) generateDocument(
	ctx context.Context,
	kind openv1.SitemapDocumentKind,
) (*sitemapRenderedDocument, error) {
	site, err := s.loadSiteContext(ctx)
	if err != nil {
		return nil, err
	}

	generatedAt := time.Now().UTC()

	switch kind {
	case openv1.SitemapDocumentKind_SITEMAP_DOCUMENT_KIND_INDEX:
		content := renderSitemapIndexXML([]sitemapIndexRow{
			{Loc: joinURL(site.CanonicalOrigin, "/sitemaps/pages.xml")},
			{Loc: joinURL(site.CanonicalOrigin, "/sitemaps/post.xml")},
			{Loc: joinURL(site.CanonicalOrigin, "/sitemaps/work.xml")},
			{Loc: joinURL(site.CanonicalOrigin, "/sitemaps/taxonomy.xml")},
		})
		return &sitemapRenderedDocument{
			Content:     content,
			ContentType: sitemapXMLContentType,
			GeneratedAt: generatedAt,
		}, nil
	case openv1.SitemapDocumentKind_SITEMAP_DOCUMENT_KIND_PAGE:
		rows, queryErr := s.buildPageRows(ctx, site)
		if queryErr != nil {
			return nil, queryErr
		}
		content := renderURLSetXML(rows)
		return &sitemapRenderedDocument{Content: content, ContentType: sitemapXMLContentType, GeneratedAt: generatedAt}, nil
	case openv1.SitemapDocumentKind_SITEMAP_DOCUMENT_KIND_POST:
		rows, queryErr := s.buildPostRows(ctx, site.CanonicalOrigin)
		if queryErr != nil {
			return nil, queryErr
		}
		content := renderURLSetXML(rows)
		return &sitemapRenderedDocument{Content: content, ContentType: sitemapXMLContentType, GeneratedAt: generatedAt}, nil
	case openv1.SitemapDocumentKind_SITEMAP_DOCUMENT_KIND_WORK:
		rows, queryErr := s.buildWorkRows(ctx, site.CanonicalOrigin)
		if queryErr != nil {
			return nil, queryErr
		}
		content := renderURLSetXML(rows)
		return &sitemapRenderedDocument{Content: content, ContentType: sitemapXMLContentType, GeneratedAt: generatedAt}, nil
	case openv1.SitemapDocumentKind_SITEMAP_DOCUMENT_KIND_TAXONOMY:
		rows, queryErr := s.buildTaxonomyRows(ctx, site.CanonicalOrigin)
		if queryErr != nil {
			return nil, queryErr
		}
		content := renderURLSetXML(rows)
		return &sitemapRenderedDocument{Content: content, ContentType: sitemapXMLContentType, GeneratedAt: generatedAt}, nil
	default:
		return nil, errs.InvalidArgumentMsg(fmt.Sprintf("unsupported sitemap kind: %s", kind.String()))
	}
}

func (s *SitemapService) loadSiteContext(ctx context.Context) (*sitemapSiteContext, error) {
	stored, err := s.store.LoadSiteContext(ctx)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if s.canonicalOrigin == "" {
		return nil, errs.Internal(fmt.Errorf("SITE_ORIGIN is empty"))
	}
	return &sitemapSiteContext{
		CanonicalOrigin: s.canonicalOrigin,
		HomepagePageID:  stored.HomepagePageID,
	}, nil
}

func (s *SitemapService) buildPageRows(
	ctx context.Context,
	site *sitemapSiteContext,
) ([]sitemapURLRow, error) {
	rows := []sitemapURLRow{
		{Loc: site.CanonicalOrigin, LastMod: s.loadHomepageLastModified(ctx, site.HomepagePageID)},
		{Loc: joinURL(site.CanonicalOrigin, "/privacy")},
		{Loc: joinURL(site.CanonicalOrigin, "/privacy/history")},
		{Loc: joinURL(site.CanonicalOrigin, "/terms")},
		{Loc: joinURL(site.CanonicalOrigin, "/terms/history")},
		{Loc: joinURL(site.CanonicalOrigin, "/sitemap")},
	}

	privacyHistory, err := s.store.ListPrivacyHistory(ctx)
	if err != nil {
		return nil, errs.Internal(err)
	}
	rows = append(rows, buildEntryRows(site.CanonicalOrigin, "/privacy/history/", privacyHistory)...)

	termsHistory, err := s.store.ListTermsHistory(ctx)
	if err != nil {
		return nil, errs.Internal(err)
	}
	rows = append(rows, buildEntryRows(site.CanonicalOrigin, "/terms/history/", termsHistory)...)

	pages, err := s.store.ListPages(ctx)
	if err != nil {
		return nil, errs.Internal(err)
	}

	reservedSlugs := map[string]struct{}{
		"privacy": {},
		"terms":   {},
		"sitemap": {},
	}

	for _, page := range pages {
		slug := strings.TrimSpace(valueOrEmpty(page.Slug))
		if slug == "" || slug == "/" {
			continue
		}
		if _, reserved := reservedSlugs[slug]; reserved {
			continue
		}
		rows = append(rows, sitemapURLRow{
			Loc:     joinURL(site.CanonicalOrigin, "/"+slug),
			LastMod: chooseEntryLastModified(page),
		})
	}

	return rows, nil
}

func (s *SitemapService) buildPostRows(
	ctx context.Context,
	canonicalOrigin string,
) ([]sitemapURLRow, error) {
	posts, err := s.store.ListPosts(ctx)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return buildEntryRows(canonicalOrigin, "/posts/", posts), nil
}

func (s *SitemapService) buildWorkRows(
	ctx context.Context,
	canonicalOrigin string,
) ([]sitemapURLRow, error) {
	works, err := s.store.ListWorks(ctx)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return buildEntryRows(canonicalOrigin, "/works/", works), nil
}

func (s *SitemapService) buildTaxonomyRows(
	ctx context.Context,
	canonicalOrigin string,
) ([]sitemapURLRow, error) {
	categories, err := s.store.ListCategories(ctx)
	if err != nil {
		return nil, errs.Internal(err)
	}
	tags, err := s.store.ListTags(ctx)
	if err != nil {
		return nil, errs.Internal(err)
	}
	rows := make([]sitemapURLRow, 0, len(categories)+len(tags))
	for _, category := range categories {
		rows = append(rows, sitemapURLRow{
			Loc:     joinURL(canonicalOrigin, "/category/"+valueOrEmpty(category.Slug)),
			LastMod: chooseEntryLastModified(category),
		})
	}
	for _, tag := range tags {
		rows = append(rows, sitemapURLRow{
			Loc:     joinURL(canonicalOrigin, "/tag/"+valueOrEmpty(tag.Slug)),
			LastMod: chooseEntryLastModified(tag),
		})
	}

	return rows, nil
}

func (s *SitemapService) loadHomepageLastModified(
	ctx context.Context,
	homepagePageID *string,
) *time.Time {
	if homepagePageID == nil || *homepagePageID == "" {
		return nil
	}
	page, err := s.store.LoadHomepage(ctx, *homepagePageID)
	if err != nil {
		slog.Warn("failed to load homepage last modified for sitemap", "page_id", *homepagePageID, "error", err)
		return nil
	}
	if page == nil {
		return nil
	}
	return chooseEntryLastModified(*page)
}

func buildEntryRows(origin, pathPrefix string, entries []sitemapdomain.Entry) []sitemapURLRow {
	rows := make([]sitemapURLRow, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, sitemapURLRow{
			Loc:     joinURL(origin, pathPrefix+slugOrID(entry.Slug, entry.ID)),
			LastMod: chooseEntryLastModified(entry),
		})
	}
	return rows
}

func chooseEntryLastModified(entry sitemapdomain.Entry) *time.Time {
	createdAt := time.Time{}
	if entry.CreatedAt != nil {
		createdAt = *entry.CreatedAt
	}
	return chooseNullableLastModified(entry.UpdatedAt, entry.PublishedAt, createdAt)
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func renderURLSetXML(rows []sitemapURLRow) string {
	document := urlSetDocument{
		Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9",
		URLs:  make([]urlSetDocumentURL, 0, len(rows)),
	}

	for _, row := range rows {
		document.URLs = append(document.URLs, urlSetDocumentURL{
			Loc:     row.Loc,
			LastMod: formatLastModified(row.LastMod),
		})
	}

	payload, _ := xml.MarshalIndent(document, "", "  ")
	return xml.Header + string(payload)
}

func renderSitemapIndexXML(rows []sitemapIndexRow) string {
	document := sitemapIndexDocument{
		Xmlns:    "http://www.sitemaps.org/schemas/sitemap/0.9",
		Sitemaps: make([]sitemapIndexDocumentEntry, 0, len(rows)),
	}

	for _, row := range rows {
		document.Sitemaps = append(document.Sitemaps, sitemapIndexDocumentEntry(row))
	}

	payload, _ := xml.MarshalIndent(document, "", "  ")
	return xml.Header + string(payload)
}

func formatLastModified(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func normalizeCanonicalOrigin(value string) string {
	trimmed := strings.TrimSpace(value)
	return strings.TrimRight(trimmed, "/")
}

func joinURL(origin, path string) string {
	base := normalizeCanonicalOrigin(origin)
	if path == "" || path == "/" {
		return base
	}
	return base + "/" + strings.TrimLeft(path, "/")
}

func slugOrID(slug *string, id string) string {
	if slug == nil {
		return id
	}
	trimmed := strings.TrimSpace(*slug)
	if trimmed == "" {
		return id
	}
	return trimmed
}

func chooseNullableLastModified(
	updatedAt *time.Time,
	publishedAt *time.Time,
	createdAt time.Time,
) *time.Time {
	if updatedAt != nil && !updatedAt.IsZero() {
		return updatedAt
	}
	if publishedAt != nil && !publishedAt.IsZero() {
		return publishedAt
	}
	if createdAt.IsZero() {
		return nil
	}
	return &createdAt
}

func makeSitemapSnapshotKey(kind openv1.SitemapDocumentKind) string {
	return fmt.Sprintf("%s:%s", sitemapSnapshotKeyPrefix, strings.ToLower(kind.String()))
}

func newSitemapMetrics() sitemapMetrics {
	meter := otel.Meter(sharedtelemetry.ServiceBackend.Instrumentation("public/sitemap"))
	metrics := sitemapMetrics{}

	responseCounter, err := meter.Int64Counter(
		"public_sitemap_responses_total",
		otelmetric.WithDescription("Counts crawler-facing sitemap responses by kind and freshness status."),
	)
	if err != nil {
		slog.Warn("failed to create sitemap response counter", "error", err)
	} else {
		metrics.responseCounter = responseCounter
	}

	generationFailureCounter, err := meter.Int64Counter(
		"public_sitemap_generation_failures_total",
		otelmetric.WithDescription("Counts sitemap generation failures before stale fallback or unavailability handling."),
	)
	if err != nil {
		slog.Warn("failed to create sitemap generation failure counter", "error", err)
	} else {
		metrics.generationFailureCounter = generationFailureCounter
	}

	staleAgeHistogram, err := meter.Int64Histogram(
		"public_sitemap_stale_snapshot_age_seconds",
		otelmetric.WithDescription("Records the age in seconds of stale sitemap snapshots served to crawlers."),
	)
	if err != nil {
		slog.Warn("failed to create sitemap stale age histogram", "error", err)
	} else {
		metrics.staleAgeHistogram = staleAgeHistogram
	}

	return metrics
}

func (s *SitemapService) recordGenerationFailureMetric(
	ctx context.Context,
	kind openv1.SitemapDocumentKind,
) {
	if s.metrics.generationFailureCounter == nil {
		return
	}

	s.metrics.generationFailureCounter.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("kind", strings.ToLower(kind.String())),
	))
}

func (s *SitemapService) recordFreshResponseMetric(
	ctx context.Context,
	kind openv1.SitemapDocumentKind,
) {
	if s.metrics.responseCounter == nil {
		return
	}

	s.metrics.responseCounter.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("kind", strings.ToLower(kind.String())),
		attribute.String("status", "fresh"),
	))
}

func (s *SitemapService) recordStaleResponseMetric(
	ctx context.Context,
	kind openv1.SitemapDocumentKind,
	generatedAt time.Time,
) {
	if s.metrics.responseCounter != nil {
		s.metrics.responseCounter.Add(ctx, 1, otelmetric.WithAttributes(
			attribute.String("kind", strings.ToLower(kind.String())),
			attribute.String("status", "stale"),
		))
	}

	if s.metrics.staleAgeHistogram != nil {
		s.metrics.staleAgeHistogram.Record(
			ctx,
			int64(time.Since(generatedAt).Seconds()),
			otelmetric.WithAttributes(attribute.String("kind", strings.ToLower(kind.String()))),
		)
	}
}

func (s *SitemapService) recordUnavailableResponseMetric(
	ctx context.Context,
	kind openv1.SitemapDocumentKind,
) {
	if s.metrics.responseCounter == nil {
		return
	}

	s.metrics.responseCounter.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("kind", strings.ToLower(kind.String())),
		attribute.String("status", "unavailable"),
	))
}
