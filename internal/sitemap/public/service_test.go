package public

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sitemapdomain "github.com/echovisionlab/geul-api/internal/sitemap"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestSitemapURLHelpersNormalizeAndFallback(t *testing.T) {
	t.Parallel()

	if got := joinURL("https://www.example.test/", ""); got != "https://www.example.test" {
		t.Fatalf("expected empty path to return normalized origin, got %q", got)
	}
	if got := joinURL("https://www.example.test/", "/posts/example"); got != "https://www.example.test/posts/example" {
		t.Fatalf("expected joined path, got %q", got)
	}

	slug := "  public-slug  "
	if got := slugOrID(&slug, "fallback-id"); got != "public-slug" {
		t.Fatalf("expected trimmed slug, got %q", got)
	}
	blankSlug := "   "
	if got := slugOrID(&blankSlug, "fallback-id"); got != "fallback-id" {
		t.Fatalf("expected blank slug to fall back to id, got %q", got)
	}
	if got := slugOrID(nil, "fallback-id"); got != "fallback-id" {
		t.Fatalf("expected nil slug to fall back to id, got %q", got)
	}
}

func TestSitemapLastModifiedHelpersPreferAvailableTimestamps(t *testing.T) {
	t.Parallel()

	published := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := published.Add(time.Hour)
	created := published.Add(-time.Hour)

	if got := chooseNullableLastModified(&updated, &published, created); got == nil || !got.Equal(updated) {
		t.Fatalf("expected nullable updated timestamp, got %v", got)
	}
	if got := chooseNullableLastModified(nil, &published, created); got == nil || !got.Equal(published) {
		t.Fatalf("expected nullable published timestamp, got %v", got)
	}
	if got := chooseNullableLastModified(nil, nil, created); got == nil || !got.Equal(created) {
		t.Fatalf("expected created timestamp, got %v", got)
	}
	if got := chooseNullableLastModified(nil, nil, time.Time{}); got != nil {
		t.Fatalf("expected zero created timestamp to return nil, got %v", got)
	}
}

func TestSitemapServiceGetDocumentRendersFreshIndex(t *testing.T) {
	store := &recordingSitemapStore{}
	svc := NewSitemapService(store, "https://www.example.test")

	resp, err := svc.GetDocument(context.Background(), connect.NewRequest(&openv1.GetSitemapDocumentRequest{
		Kind: openv1.SitemapDocumentKind_SITEMAP_DOCUMENT_KIND_INDEX,
	}))
	require.NoError(t, err)
	require.Equal(t, openv1.SitemapDocumentStatus_SITEMAP_DOCUMENT_STATUS_FRESH, resp.Msg.Status)
	require.Equal(t, sitemapXMLContentType, resp.Msg.ContentType)
	require.Contains(t, resp.Msg.Content, "<sitemapindex")
	require.Contains(t, resp.Msg.Content, "https://www.example.test/sitemaps/post.xml")
	require.NotNil(t, resp.Msg.GeneratedAt)
	require.True(t, store.saveCalled)

	_, err = svc.GetDocument(context.Background(), connect.NewRequest(&openv1.GetSitemapDocumentRequest{}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = svc.GetDocument(context.Background(), connect.NewRequest(&openv1.GetSitemapDocumentRequest{
		Kind: openv1.SitemapDocumentKind(999),
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSitemapServiceUsesSnapshotFallbackWhenGenerationFails(t *testing.T) {
	store := &recordingSitemapStore{
		snapshot: &sitemapdomain.Snapshot{
			Content:     "<urlset>stale</urlset>",
			ContentType: sitemapXMLContentType,
			GeneratedAt: time.Now().Add(-time.Hour),
		},
	}
	svc := NewSitemapService(store, "")

	stale, err := svc.GetDocument(context.Background(), connect.NewRequest(&openv1.GetSitemapDocumentRequest{
		Kind: openv1.SitemapDocumentKind_SITEMAP_DOCUMENT_KIND_PAGE,
	}))
	require.NoError(t, err)
	require.Equal(t, openv1.SitemapDocumentStatus_SITEMAP_DOCUMENT_STATUS_STALE, stale.Msg.Status)
	require.Equal(t, "<urlset>stale</urlset>", stale.Msg.Content)
	require.True(t, store.loadCalled)

	unavailableStore := &recordingSitemapStore{}
	unavailableSvc := NewSitemapService(unavailableStore, "")
	unavailable, err := unavailableSvc.GetDocument(context.Background(), connect.NewRequest(&openv1.GetSitemapDocumentRequest{
		Kind: openv1.SitemapDocumentKind_SITEMAP_DOCUMENT_KIND_PAGE,
	}))
	require.NoError(t, err)
	require.Equal(t, openv1.SitemapDocumentStatus_SITEMAP_DOCUMENT_STATUS_UNAVAILABLE, unavailable.Msg.Status)
	require.Equal(t, sitemapTextContentType, unavailable.Msg.ContentType)
	require.NotNil(t, unavailable.Msg.RetryAfterSeconds)
}

func TestSitemapServiceFreshResponseIgnoresSnapshotSaveFailure(t *testing.T) {
	store := &recordingSitemapStore{saveErr: errors.New("snapshot store unavailable")}
	svc := NewSitemapService(store, "https://www.example.test")

	resp, err := svc.GetDocument(context.Background(), connect.NewRequest(&openv1.GetSitemapDocumentRequest{
		Kind: openv1.SitemapDocumentKind_SITEMAP_DOCUMENT_KIND_INDEX,
	}))
	require.NoError(t, err)
	require.Equal(t, openv1.SitemapDocumentStatus_SITEMAP_DOCUMENT_STATUS_FRESH, resp.Msg.Status)
	require.Contains(t, resp.Msg.Content, "<sitemapindex")
	require.True(t, store.saveCalled)
}

func TestSitemapServiceUnavailableWhenGenerationAndSnapshotLoadFail(t *testing.T) {
	store := &recordingSitemapStore{loadErr: errors.New("snapshot load unavailable")}
	svc := NewSitemapService(store, "")

	resp, err := svc.GetDocument(context.Background(), connect.NewRequest(&openv1.GetSitemapDocumentRequest{
		Kind: openv1.SitemapDocumentKind_SITEMAP_DOCUMENT_KIND_PAGE,
	}))
	require.NoError(t, err)
	require.Equal(t, openv1.SitemapDocumentStatus_SITEMAP_DOCUMENT_STATUS_UNAVAILABLE, resp.Msg.Status)
	require.Equal(t, sitemapTextContentType, resp.Msg.ContentType)
	require.True(t, store.loadCalled)
}

func TestNewSitemapServiceRequiresStoreAndNormalizesOrigin(t *testing.T) {
	t.Parallel()

	require.Panics(t, func() { NewSitemapService(nil, "https://www.example.test") })
	store := &recordingSitemapStore{}
	svc := NewSitemapService(store, "https://www.example.test/")
	require.Same(t, store, svc.store)
	require.Equal(t, "https://www.example.test", svc.canonicalOrigin)
}

type recordingSitemapStore struct {
	siteContext sitemapdomain.SiteContext
	homepage    *sitemapdomain.Entry
	pages       []sitemapdomain.Entry
	privacy     []sitemapdomain.Entry
	terms       []sitemapdomain.Entry
	posts       []sitemapdomain.Entry
	works       []sitemapdomain.Entry
	categories  []sitemapdomain.Entry
	tags        []sitemapdomain.Entry
	snapshot    *sitemapdomain.Snapshot
	loadErr     error
	saveErr     error
	loadCalled  bool
	saveCalled  bool
}

func (s *recordingSitemapStore) LoadSiteContext(context.Context) (sitemapdomain.SiteContext, error) {
	return s.siteContext, nil
}

func (s *recordingSitemapStore) LoadHomepage(context.Context, string) (*sitemapdomain.Entry, error) {
	return s.homepage, nil
}

func (s *recordingSitemapStore) ListPages(context.Context) ([]sitemapdomain.Entry, error) {
	return s.pages, nil
}

func (s *recordingSitemapStore) ListPrivacyHistory(context.Context) ([]sitemapdomain.Entry, error) {
	return s.privacy, nil
}

func (s *recordingSitemapStore) ListTermsHistory(context.Context) ([]sitemapdomain.Entry, error) {
	return s.terms, nil
}

func (s *recordingSitemapStore) ListPosts(context.Context) ([]sitemapdomain.Entry, error) {
	return s.posts, nil
}

func (s *recordingSitemapStore) ListWorks(context.Context) ([]sitemapdomain.Entry, error) {
	return s.works, nil
}

func (s *recordingSitemapStore) ListCategories(context.Context) ([]sitemapdomain.Entry, error) {
	return s.categories, nil
}

func (s *recordingSitemapStore) ListTags(context.Context) ([]sitemapdomain.Entry, error) {
	return s.tags, nil
}

func (s *recordingSitemapStore) LoadSnapshot(context.Context, string) (*sitemapdomain.Snapshot, error) {
	s.loadCalled = true
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.snapshot, nil
}

func (s *recordingSitemapStore) SaveSnapshot(context.Context, string, *sitemapdomain.Snapshot) (bool, error) {
	s.saveCalled = true
	return s.saveErr == nil, s.saveErr
}
