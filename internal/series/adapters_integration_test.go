//go:build integration

package series_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/series"
	"github.com/echovisionlab/geul-api/internal/series/seriestest"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	// The Series transaction carries the Post, media, OG and Menu adapters of
	// other domains. Their SQL is theirs, but whether it runs in the Series
	// transaction and sees its rows is the Series store's binding, which only
	// PostgreSQL shows. SpiceDB and the identity fence stay fakes.
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
)

func TestSeriesTransactionsBindTheOtherDomainsIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	spiceDB := seriestest.NewAuthorizer(authztest.AllowAll())
	service := newIntegrationService(t, db, integrationDeps{spiceDB: spiceDB, fence: &identitystatetest.Fence{}, postAccess: allowedPostEdits{}})
	ctx := principalContext(t, uuid.NewString(), uuid.NewString())

	created, err := service.CreateSeries(ctx, series.CreateSeriesCommand{Title: "Bound Series", Slug: text("bound-original")})
	require.NoError(t, err)
	seriesID := created.Series.ID
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO series_translation (entity_id, locale, title, created_at, updated_at)
		VALUES (?::uuid, 'ko', '묶인 시리즈', ?, ?)`, seriesID, now, now).Error)

	// Membership moves Post rows through Post's own SQL, which advances
	// updated_at only for the Posts that moved.
	stale := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	posts := []string{
		orytest.SeedPostBaseRow(t, db, managev1.PostStatus_POST_STATUS_DRAFT.String()),
		orytest.SeedPostBaseRow(t, db, managev1.PostStatus_POST_STATUS_DRAFT.String()),
		orytest.SeedPostBaseRow(t, db, managev1.PostStatus_POST_STATUS_DRAFT.String()),
	}
	backdate := func() {
		require.NoError(t, db.Exec(`UPDATE post SET updated_at = ? WHERE id IN ?`, stale, posts).Error)
	}
	advanced := func(postID string) bool {
		var updatedAt time.Time
		require.NoError(t, db.Raw(`SELECT updated_at FROM post WHERE id = ?`, postID).Scan(&updatedAt).Error)
		return updatedAt.After(stale)
	}
	backdate()
	for _, postID := range posts {
		require.NoError(t, service.AssignPostToSeries(ctx, series.AssignPostToSeriesCommand{PostID: postID, SeriesID: seriesID}))
	}
	require.True(t, advanced(posts[0]) && advanced(posts[1]) && advanced(posts[2]))
	backdate()
	require.NoError(t, service.ReorderSeriesPosts(ctx, series.ReorderSeriesPostsCommand{SeriesID: seriesID, PostIDs: []string{posts[1], posts[0], posts[2]}}))
	require.True(t, advanced(posts[0]) && advanced(posts[1]))
	require.False(t, advanced(posts[2]), "a Post that keeps its position keeps its updated_at")
	listed, err := service.ListSeriesPosts(ctx, series.ListSeriesPostsQuery{SeriesID: seriesID})
	require.NoError(t, err)
	require.Equal(t, posts[1], listed[0].ID)

	// Removing the featured image requests a title-only OG image per locale.
	fileID := seedImageFile(t, db, "series/"+seriesID+"/featured.webp")
	replacementID := seedImageFile(t, db, "series/"+seriesID+"/replacement.webp")
	_, err = service.SetSeriesFeaturedImage(ctx, series.SetSeriesFeaturedImageCommand{SeriesID: seriesID, FileID: fileID})
	require.NoError(t, err)
	changed, err := service.SetSeriesFeaturedImage(ctx, series.SetSeriesFeaturedImageCommand{SeriesID: seriesID, FileID: replacementID})
	require.NoError(t, err)
	require.NotNil(t, changed.Asset)
	runID, err := service.DeleteSeriesFeaturedImage(ctx, series.DeleteSeriesFeaturedImageCommand{SeriesID: seriesID})
	require.NoError(t, err)
	require.NotNil(t, runID)
	var generations []model.OgGeneration
	require.NoError(t, db.Where("run_id = ?", *runID).Order("request_sequence ASC").Find(&generations).Error)
	locales := make([]string, 0, len(generations))
	for _, generation := range generations {
		var snapshot struct {
			Locale        *string         `json:"locale"`
			FeaturedImage json.RawMessage `json:"featured_image"`
		}
		require.NoError(t, json.Unmarshal(generation.EntitySnapshot, &snapshot))
		require.Empty(t, snapshot.FeaturedImage)
		locales = append(locales, *snapshot.Locale)
	}
	require.ElementsMatch(t, []string{"en", "ko"}, locales)
	_, err = service.SetSeriesFeaturedImage(ctx, series.SetSeriesFeaturedImageCommand{SeriesID: seriesID, FileID: fileID})
	require.NoError(t, err)

	// A rename rewrites the Menu links in the same transaction.
	menuID, menuDocumentID := uuid.NewString(), uuid.NewString()
	originalSlug := "bound-original"
	items, err := json.Marshal([]model.MenuItem{{ID: "series-link", Label: "Series", LinkType: "series", TargetID: &seriesID, TargetSlug: &originalSlug}})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile, revision, created_at, updated_at) VALUES (?::uuid, 'compact', ?::uuid, NOW(), NOW())`, menuDocumentID, uuid.NewString()).Error)
	require.NoError(t, db.Create(&model.Menu{ID: menuID, ContentDocumentID: menuDocumentID, SourceLocale: "en", Name: "Series menu", Items: items, CreatedAt: now, UpdatedAt: now}).Error)
	_, err = service.UpdateSeries(ctx, series.UpdateSeriesCommand{ID: seriesID, Slug: text("bound-renamed")})
	require.NoError(t, err)
	var menu model.Menu
	require.NoError(t, db.First(&menu, "id = ?", menuID).Error)
	var storedItems []model.MenuItem
	require.NoError(t, json.Unmarshal(menu.Items, &storedItems))
	require.Equal(t, "bound-renamed", *storedItems[0].TargetSlug)

	// Deletion clears the Posts, releases the bindings and keeps the Files.
	spiceDB.Relationships[seriesID] = []policyv1.RelationshipMutation{must(policyv1.PostSeries.TouchPolicy(seriesID))}
	require.NoError(t, service.DeleteSeries(ctx, series.DeleteSeriesCommand{ID: seriesID}))
	requireCount(t, db, 0, "series", "id = ?", seriesID)
	requireCount(t, db, 0, "post", "series_id = ?", seriesID)
	requireCount(t, db, 0, "public_asset_binding", "owner_type = ? AND owner_id = ?", "series", seriesID)
	requireCount(t, db, 0, "content_document", "id = ?", created.Series.ContentDocumentID)
	requireCount(t, db, 2, "file", "id IN ?", []string{fileID, replacementID})
	require.NoError(t, db.First(&menu, "id = ?", menuID).Error)
	require.NoError(t, json.Unmarshal(menu.Items, &storedItems))
	require.Empty(t, storedItems, "deletion removes the Menu link")
}

type queryCounter struct {
	logger.Interface
	count *atomic.Int64
}

func (l queryCounter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	l.count.Add(1)
	l.Interface.Trace(ctx, begin, fc, err)
}

// The admin list is one read snapshot plus a fixed number of batched
// projections, whatever the page size.
func TestSeriesAdminListQueryBudgetDoesNotGrowPerRowIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	for index := range 12 {
		seedSeriesRow(t, db, "Query Series "+suffix, "query-series-"+suffix+"-"+string(rune('a'+index)), model.SeriesStatusDraft)
	}
	var queries atomic.Int64
	counted := db.Session(&gorm.Session{Logger: queryCounter{Interface: db.Config.Logger, count: &queries}})
	service := newIntegrationService(t, counted, integrationDeps{spiceDB: seriestest.NewAuthorizer(authztest.AllowAll()), fence: &identitystatetest.Fence{}})
	ctx := principalContext(t, uuid.NewString(), uuid.NewString())
	listCount := func(limit int32) int64 {
		queries.Store(0)
		page, err := service.ListSeriesAdmin(ctx, series.ListSeriesAdminQuery{Page: series.Page{Limit: limit}})
		require.NoError(t, err)
		require.Len(t, page.Items, int(limit))
		return queries.Load()
	}
	one, twelve := listCount(1), listCount(12)
	require.Equal(t, one, twelve)
	require.LessOrEqual(t, twelve, int64(8))
}

// The admin filter and sort are SQL over the source copy.
func TestSeriesAdminListFiltersAndSortsBySourceTitleIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	alpha := seedSeriesRow(t, db, "Alpha "+suffix, "alpha-"+suffix, model.SeriesStatusPublished)
	seedSeriesRow(t, db, "Alphabet "+suffix, "alphabet-"+suffix, model.SeriesStatusDraft)
	seedSeriesRow(t, db, "Zulu "+suffix, "zulu-"+suffix, model.SeriesStatusPublished)
	service := newIntegrationService(t, db, integrationDeps{spiceDB: seriestest.NewAuthorizer(authztest.AllowAll()), fence: &identitystatetest.Fence{}})

	page, err := service.ListSeriesAdmin(principalContext(t, uuid.NewString(), uuid.NewString()), series.ListSeriesAdminQuery{
		Page: series.Page{Limit: 20},
		Filters: []*commonv1.FilterSpec{
			{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "ALPHA " + suffix},
			{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: string(model.SeriesStatusPublished)},
		},
		Sorts: []*commonv1.SortSpec{{Field: "title", Order: commonv1.SortOrder_SORT_ORDER_ASC}},
	})

	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, alpha, page.Items[0].Series.ID)
	require.Equal(t, "Alpha "+suffix, page.Items[0].Series.Title)
}
