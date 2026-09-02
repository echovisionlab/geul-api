//go:build integration

package public

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	sitemapadapter "github.com/echovisionlab/geul-api/internal/adapters/sitemap"
	sitemapdomain "github.com/echovisionlab/geul-api/internal/sitemap"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

var (
	sitemapIntegrationStack *testutil.BackendIntegrationStack
	sitemapIntegrationOnce  sync.Once
	sitemapIntegrationErr   error
	sitemapIntegrationMu    sync.Mutex
)

func TestMain(m *testing.M) {
	code := m.Run()
	if sitemapIntegrationStack != nil {
		if err := sitemapIntegrationStack.Close(); err != nil && code == 0 {
			fmt.Fprintf(os.Stderr, "close Sitemap integration stack: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

func TestSitemapServiceDocumentsUsePublishedReadModelEntitiesIntegration(t *testing.T) {
	db := newSitemapIntegrationDB(t)
	origin := "https://www.example.test"
	suffix := uuid.NewString()
	now := time.Now().UTC()

	pageID := insertSitemapContentDocument(t, db, "page")
	pageSlug := "sitemap-page-" + suffix
	require.NoError(t, db.Exec(`
		INSERT INTO page (id, slug, status, published_at, content_document_id)
		VALUES (?::uuid, ?, ?, ?, ?::uuid)`,
		pageID, pageSlug, managev1.PageStatus_PAGE_STATUS_PUBLISHED.String(), now, pageID,
	).Error)
	require.NoError(t, db.Exec(
		`UPDATE site_settings SET homepage_page_id = ?::uuid WHERE id = 1`, pageID,
	).Error)

	postID := insertSitemapContentDocument(t, db, "post")
	postSlug := "sitemap-post-" + suffix
	require.NoError(t, db.Exec(`
		INSERT INTO post (id, slug, status, published_at, content_document_id)
		VALUES (?::uuid, ?, ?::post_status, ?, ?::uuid)`,
		postID, postSlug, managev1.PostStatus_POST_STATUS_ARCHIVED.String(), now, postID,
	).Error)

	workID := insertSitemapContentDocument(t, db, "work")
	workSlug := "sitemap-work-" + suffix
	require.NoError(t, db.Exec(`
		INSERT INTO work (
			id, slug, type, status, published_at, year, month, is_present, content_document_id
		) VALUES (?::uuid, ?, ?::work_type, ?, ?, ?, ?, true, ?::uuid)`,
		workID, workSlug, managev1.WorkType_WORK_TYPE_PORTFOLIO.String(),
		managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(), now, 2026, 5, workID,
	).Error)

	categorySlug := "sitemap-category-" + suffix
	tagSlug := "sitemap-tag-" + suffix
	require.NoError(t, db.Exec(
		`INSERT INTO category (id, name, slug) VALUES (?::uuid, ?, ?)`,
		uuid.NewString(), "Sitemap Category "+suffix, categorySlug,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO tag (id, name, slug) VALUES (?::uuid, ?, ?)`,
		uuid.NewString(), "Sitemap Tag "+suffix, tagSlug,
	).Error)

	privacyID := insertSitemapContentDocument(t, db, "policy")
	require.NoError(t, db.Exec(`
		INSERT INTO privacy_history (
			id, version, title, content, status, effective_from, content_document_id
		) VALUES (
			?::uuid, (SELECT COALESCE(MAX(version), 0) + 1 FROM privacy_history),
			?, ?, ?, ?, ?::uuid
		)`,
		privacyID, "Sitemap Privacy "+suffix, "Privacy", managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(), now, privacyID,
	).Error)

	termsID := insertSitemapContentDocument(t, db, "policy")
	require.NoError(t, db.Exec(`
		INSERT INTO terms_history (
			id, version, title, content, status, effective_from, content_document_id
		) VALUES (
			?::uuid, (SELECT COALESCE(MAX(version), 0) + 1 FROM terms_history),
			?, ?, ?, ?, ?::uuid
		)`,
		termsID, "Sitemap Terms "+suffix, "Terms", managev1.TermsStatus_TERMS_STATUS_ACTIVE.String(), now, termsID,
	).Error)

	store := sitemapadapter.NewPostgresStore(db)
	svc := NewSitemapService(store, origin)
	pageDoc := assertSitemapDocumentContains(t, svc, openv1.SitemapDocumentKind_SITEMAP_DOCUMENT_KIND_PAGE,
		origin+"/"+pageSlug,
		origin+"/privacy/history/"+privacyID,
		origin+"/terms/history/"+termsID,
	)
	require.Equal(t, 1, strings.Count(pageDoc.Msg.Content, "<loc>"+origin+"</loc>"))
	require.Equal(t, 1, strings.Count(pageDoc.Msg.Content, "<loc>"+origin+"/privacy</loc>"))
	require.Equal(t, 1, strings.Count(pageDoc.Msg.Content, "<loc>"+origin+"/terms</loc>"))
	require.Equal(t, 1, strings.Count(pageDoc.Msg.Content, "<loc>"+origin+"/sitemap</loc>"))
	assertSitemapDocumentContains(t, svc, openv1.SitemapDocumentKind_SITEMAP_DOCUMENT_KIND_POST, origin+"/posts/"+postSlug)
	assertSitemapDocumentContains(t, svc, openv1.SitemapDocumentKind_SITEMAP_DOCUMENT_KIND_WORK, origin+"/works/"+workSlug)
	assertSitemapDocumentContains(t, svc, openv1.SitemapDocumentKind_SITEMAP_DOCUMENT_KIND_TAXONOMY,
		origin+"/category/"+categorySlug,
		origin+"/tag/"+tagSlug,
	)
}

func TestPostgresSitemapSnapshotStoreRoundTripIntegration(t *testing.T) {
	db := newSitemapIntegrationDB(t)
	store := sitemapadapter.NewPostgresStore(db)
	key := "integration:" + uuid.NewString()
	generatedAt := time.Date(2026, 5, 8, 1, 2, 3, 0, time.UTC)

	missing, err := store.LoadSnapshot(t.Context(), key)
	require.NoError(t, err)
	require.Nil(t, missing)

	want := &sitemapdomain.Snapshot{
		Content:     "<urlset>fresh</urlset>",
		ContentType: sitemapXMLContentType,
		GeneratedAt: generatedAt,
	}
	written, err := store.SaveSnapshot(t.Context(), key, want)
	require.NoError(t, err)
	require.True(t, written)

	writeCount := 1
	for _, duplicateGeneratedAt := range []time.Time{
		generatedAt.Add(time.Minute),
		generatedAt.Add(2 * time.Minute),
	} {
		written, saveErr := store.SaveSnapshot(t.Context(), key, &sitemapdomain.Snapshot{
			Content:     want.Content,
			ContentType: want.ContentType,
			GeneratedAt: duplicateGeneratedAt,
		})
		require.NoError(t, saveErr)
		if written {
			writeCount++
		}
	}
	require.Equal(t, 1, writeCount)

	got, err := store.LoadSnapshot(t.Context(), key)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, want.Content, got.Content)
	require.Equal(t, want.ContentType, got.ContentType)
	require.True(t, want.GeneratedAt.Equal(got.GeneratedAt))

	written, err = store.SaveSnapshot(t.Context(), key, &sitemapdomain.Snapshot{
		Content:     "<urlset>changed</urlset>",
		ContentType: want.ContentType,
		GeneratedAt: generatedAt.Add(3 * time.Minute),
	})
	require.NoError(t, err)
	require.True(t, written)
}

func newSitemapIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	sitemapIntegrationMu.Lock()
	t.Cleanup(sitemapIntegrationMu.Unlock)

	sitemapIntegrationOnce.Do(func() {
		sitemapIntegrationStack, sitemapIntegrationErr = testutil.StartBackendIntegrationStack(context.Background())
	})
	require.NoError(t, sitemapIntegrationErr)
	require.NoError(t, testutil.ResetBackendIntegrationState(t.Context(), sitemapIntegrationStack))
	t.Cleanup(func() {
		require.NoError(t, testutil.ResetBackendIntegrationState(context.Background(), sitemapIntegrationStack))
	})
	return sitemapIntegrationStack.Postgres.DB
}

func insertSitemapContentDocument(t *testing.T, db *gorm.DB, profile string) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile) VALUES (?::uuid, ?)`, id, profile,
	).Error)
	return id
}

func assertSitemapDocumentContains(
	t *testing.T,
	svc *SitemapService,
	kind openv1.SitemapDocumentKind,
	expectedURLs ...string,
) *connect.Response[openv1.GetSitemapDocumentResponse] {
	t.Helper()

	resp, err := svc.GetDocument(t.Context(), connect.NewRequest(&openv1.GetSitemapDocumentRequest{Kind: kind}))
	require.NoError(t, err)
	require.Equal(t, openv1.SitemapDocumentStatus_SITEMAP_DOCUMENT_STATUS_FRESH, resp.Msg.Status)
	require.Equal(t, sitemapXMLContentType, resp.Msg.ContentType)
	for _, expectedURL := range expectedURLs {
		require.Contains(t, resp.Msg.Content, expectedURL)
	}
	return resp
}
