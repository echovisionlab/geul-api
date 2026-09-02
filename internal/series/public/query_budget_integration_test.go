//go:build integration

// Series public query-budget coverage belongs to the Series public package.

package public_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	seriespublicadapter "github.com/echovisionlab/geul-api/internal/adapters/series/public"
	seriespublic "github.com/echovisionlab/geul-api/internal/series/public"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

type publicSeriesQueryCounter struct {
	logger.Interface
	count *atomic.Int64
}

func (l publicSeriesQueryCounter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	l.count.Add(1)
	l.Interface.Trace(ctx, begin, fc, err)
}

func TestPublicSeriesListQueryBudgetDoesNotGrowPerRowIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	suffix := uuid.NewString()
	for index := 0; index < 12; index++ {
		imageFileID, _ := seedCanonicalPublicFileFixture(t, db, fmt.Sprintf("series-%02d.webp", index), "image/webp", "image")
		_, ogAssetID := seedCanonicalPublicFileFixture(t, db, fmt.Sprintf("series-%02d-og.webp", index), "image/webp", "og")
		seedLocalizedPublicSeries(
			t, db, uuid.NewString(), fmt.Sprintf("Public Series %02d %s", index, suffix),
			fmt.Sprintf("public-series-%02d-%s", index, suffix),
			managev1.SeriesStatus_SERIES_STATUS_PUBLISHED.String(), &imageFileID, &ogAssetID,
		)
	}

	var queryCount atomic.Int64
	countedDB := db.Session(&gorm.Session{Logger: publicSeriesQueryCounter{
		Interface: db.Config.Logger,
		count:     &queryCount,
	}})
	service := seriespublic.NewSeriesService(seriespublicadapter.NewPublicReader(countedDB, "https://cdn.example.com"))
	listCount := func(limit int32) int64 {
		queryCount.Store(0)
		response, err := service.List(context.Background(), connect.NewRequest(&openv1.ListSeriesRequest{
			Pagination: &commonv1.PaginationRequest{Limit: limit},
		}))
		require.NoError(t, err)
		require.NotEmpty(t, response.Msg.Series)
		for _, item := range response.Msg.Series {
			require.NotNil(t, item.FeaturedImageAsset)
			require.NotNil(t, item.OgAsset)
		}
		return queryCount.Load()
	}
	oneRowQueries := listCount(1)
	twelveRowQueries := listCount(12)
	require.Equal(t, oneRowQueries, twelveRowQueries)
	require.LessOrEqual(t, twelveRowQueries, int64(8))
}

func TestPublicSeriesPostCountIncludesPublishedAndArchivedIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	identityID := uuid.NewString()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Series count author")
	seriesID := uuid.NewString()
	suffix := uuid.NewString()
	seedLocalizedPublicSeries(
		t, db, seriesID, "Series count "+suffix, "series-count-"+suffix,
		managev1.SeriesStatus_SERIES_STATUS_PUBLISHED.String(), nil, nil,
	)

	for index, status := range []string{
		managev1.PostStatus_POST_STATUS_PUBLISHED.String(),
		managev1.PostStatus_POST_STATUS_ARCHIVED.String(),
		managev1.PostStatus_POST_STATUS_DRAFT.String(),
	} {
		postID := uuid.NewString()
		documentID := uuid.NewString()
		require.NoError(t, db.Exec(`
			INSERT INTO content_document (id, profile)
			VALUES (?::uuid, 'post')
		`, documentID).Error)
		require.NoError(t, db.Exec(`
			INSERT INTO post (id, status, slug, series_id, series_order, published_at, comments_enabled, created_at, updated_at, content_document_id)
			VALUES (?::uuid, ?, ?, ?::uuid, ?, NOW(), TRUE, NOW(), NOW(), ?::uuid)
		`, postID, status, fmt.Sprintf("series-count-post-%d-%s", index, suffix), seriesID, index, documentID).Error)
		require.NoError(t, db.Exec(`
			INSERT INTO post_author (post_id, member_id, created_at)
			VALUES (?::uuid, ?::uuid, NOW())
		`, postID, memberID).Error)
	}

	response, err := seriespublic.NewSeriesService(seriespublicadapter.NewPublicReader(db, "https://cdn.example.com")).List(
		context.Background(), connect.NewRequest(&openv1.ListSeriesRequest{
			Pagination: &commonv1.PaginationRequest{Limit: 20},
		}),
	)
	require.NoError(t, err)
	var matched *openv1.Series
	for _, item := range response.Msg.Series {
		if item.Id == seriesID {
			matched = item
			break
		}
	}
	require.NotNil(t, matched)
	require.Equal(t, int32(2), matched.PostCount)
}

func TestPublicSeriesGetUsesDeterministicIDOrSlugAndConcealsDraftIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	targetID := uuid.NewString()
	otherID := uuid.NewString()
	draftID := uuid.NewString()
	seedLocalizedPublicSeries(
		t, db, targetID, "Deterministic Series", "deterministic-series",
		managev1.SeriesStatus_SERIES_STATUS_PUBLISHED.String(), nil, nil,
	)
	seedLocalizedPublicSeries(
		t, db, otherID, "UUID-looking slug Series", targetID,
		managev1.SeriesStatus_SERIES_STATUS_PUBLISHED.String(), nil, nil,
	)
	seedLocalizedPublicSeries(
		t, db, draftID, "Draft Series", "draft-series",
		managev1.SeriesStatus_SERIES_STATUS_DRAFT.String(), nil, nil,
	)

	service := seriespublic.NewSeriesService(seriespublicadapter.NewPublicReader(db, "https://cdn.example.com"))
	byID := connect.NewRequest(&openv1.GetSeriesRequest{Slug: targetID})
	byID.Header().Set("Accept-Language", "ko")
	idResponse, err := service.Get(context.Background(), byID)
	require.NoError(t, err)
	require.Equal(t, targetID, idResponse.Msg.GetSeries().GetId())
	require.Equal(t, "Deterministic Series", idResponse.Msg.GetSeries().GetTitle())
	require.Equal(t, "en", idResponse.Msg.GetSeries().GetLocalizationInfo().GetDisplayedLocale())

	bySlug, err := service.Get(context.Background(), connect.NewRequest(&openv1.GetSeriesRequest{Slug: "deterministic-series"}))
	require.NoError(t, err)
	require.Equal(t, targetID, bySlug.Msg.GetSeries().GetId())

	_, err = service.Get(context.Background(), connect.NewRequest(&openv1.GetSeriesRequest{Slug: draftID}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = service.Get(context.Background(), connect.NewRequest(&openv1.GetSeriesRequest{Slug: uuid.NewString()}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestPublicSeriesPaginationIsStableAcrossDuplicateTitlesIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	seeded := make(map[string]struct{}, 105)
	for index := 0; index < 105; index++ {
		id := uuid.NewString()
		seeded[id] = struct{}{}
		seedLocalizedPublicSeries(
			t, db, id, "Same public title", fmt.Sprintf("same-public-title-%03d", index),
			managev1.SeriesStatus_SERIES_STATUS_PUBLISHED.String(), nil, nil,
		)
	}

	service := seriespublic.NewSeriesService(seriespublicadapter.NewPublicReader(db, "https://cdn.example.com"))
	seen := make(map[string]struct{}, len(seeded))
	for offset := int32(0); ; offset += 100 {
		response, err := service.List(context.Background(), connect.NewRequest(&openv1.ListSeriesRequest{
			Pagination: &commonv1.PaginationRequest{Limit: 100, Offset: offset},
			Sorts:      []*commonv1.SortSpec{{Field: "title", Order: commonv1.SortOrder_SORT_ORDER_ASC}},
		}))
		require.NoError(t, err)
		for _, item := range response.Msg.Series {
			_, duplicate := seen[item.Id]
			require.False(t, duplicate, "stable pages must not repeat a Series")
			seen[item.Id] = struct{}{}
		}
		if !response.Msg.GetPagination().GetHasMore() {
			break
		}
	}
	require.Equal(t, seeded, seen)
}

func seedLocalizedPublicSeries(
	t *testing.T,
	db *gorm.DB,
	id string,
	title string,
	slug string,
	status string,
	featuredImageFileID *string,
	ogAssetID *string,
) {
	t.Helper()
	contentDocumentID := uuid.NewString()
	contentDocumentRevision := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO content_document (id, profile, revision)
		VALUES (?::uuid, 'compact', ?::uuid)
	`, contentDocumentID, contentDocumentRevision).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO series (id, slug, status, source_locale, content_document_id, featured_image_file_id, created_at, updated_at)
		VALUES (?::uuid, ?, ?, 'en', ?::uuid, ?::uuid, NOW(), NOW())
	`, id, slug, status, contentDocumentID, featuredImageFileID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO series_translation (
			entity_id, locale, title, created_at, updated_at, og_asset_id
		) VALUES (?::uuid, 'en', ?, NOW(), NOW(), ?::uuid)
	`, id, title, ogAssetID).Error)
}
