//go:build integration

package postpublicpostgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/post/public"
	postpublicpostgres "github.com/echovisionlab/geul-api/internal/post/public/postgres"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// These tests keep the public reads' own SQL: the public status gate of
// listing, search and the map, and the relation filters. The use cases
// above the Store run on publictest's fake in package public; the share
// link proof and localization belong to their own adapters.

type unusedFileReuse struct{}

func (unusedFileReuse) AuthorizeFileReuse(context.Context, *gorm.DB, contentblock.Document, contentblock.FullBlock, contentblock.FileReference, contentblock.File) error {
	panic("the public reads reuse no File")
}

// unused stand-ins: the reads under test reach no localization, map place
// projection or share link proof.
type (
	unusedLocalization struct {
		postpublicpostgres.LocalizationService
	}
	unusedMapPlaces struct {
		postpublicpostgres.MapPlaceProjector
	}
	unusedShareLinks struct {
		postpublicpostgres.ShareLinkValidator
	}
)

type fixture struct {
	db    *gorm.DB
	place string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	place := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO map_place (id, name, address, lat, lng) VALUES (?, 'Place', 'Road', 37.55, 126.99)`, place).Error)
	return fixture{db: db, place: place}
}

func (f fixture) harness(t *testing.T) mutationtest.Harness[public.Store] {
	t.Helper()
	contents, err := contentblock.NewGeneratedStore(unusedFileReuse{})
	require.NoError(t, err)
	units, err := postpublicpostgres.NewUnits(f.db, postpublicpostgres.Deps{
		ContentBlocks: contents, Localization: unusedLocalization{}, MapPlaces: unusedMapPlaces{}, ShareLinks: unusedShareLinks{},
	})
	require.NoError(t, err)
	return mutationtest.PostgresHarness(f.db, units)
}

// seed stores a Post of status titled title at the fixture's place, in
// category when set, and returns its ID.
func (f fixture) seed(t *testing.T, status post.Status, title, category string, publishedAt time.Time) string {
	t.Helper()
	id, document := uuid.NewString(), uuid.NewString()
	require.NoError(t, f.db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'post')`, document).Error)
	var published *time.Time
	if status != post.StatusDraft {
		published = &publishedAt
	}
	require.NoError(t, f.db.Exec(`INSERT INTO post (id, status, content_document_id, map_place_id, published_at) VALUES (?, ?, ?, ?, ?)`,
		id, string(status), document, f.place, published).Error)
	require.NoError(t, f.db.Exec(`INSERT INTO post_translation (entity_id, locale, title) VALUES (?, 'en', ?)`, id, title).Error)
	if category != "" {
		require.NoError(t, f.db.Exec(`INSERT INTO post_category (post_id, category_id) VALUES (?, ?)`, id, category).Error)
	}
	return id
}

func postIDs(posts []post.Post) []string {
	ids := make([]string, len(posts))
	for i, stored := range posts {
		ids[i] = stored.ID
	}
	return ids
}

func mapPostIDs(rows []public.MapFeatureRow) []string {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.PostID
	}
	return ids
}

func TestPublicStoreListsSearchesAndMapsOnlyPublishedAndArchivedPostsIntegration(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	category, other := uuid.NewString(), uuid.NewString()
	for _, id := range []string{category, other} {
		require.NoError(t, f.db.Exec(`INSERT INTO category (id, name, slug) VALUES (?, ?, ?)`, id, id, id).Error)
	}
	base := time.Now().UTC().Truncate(time.Second)
	f.seed(t, post.StatusDraft, "Reference Draft", category, base)
	archived := f.seed(t, post.StatusArchived, "Reference Archived", category, base.Add(-2*time.Hour))
	published := f.seed(t, post.StatusPublished, "Reference Published", category, base.Add(-time.Hour))
	elsewhere := f.seed(t, post.StatusPublished, "Reference Elsewhere", other, base)
	f.seed(t, post.StatusPublished, "Unrelated", "", base)
	h := f.harness(t)
	search := &commonv1.FilterSpec{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "reference"}
	status := func(op commonv1.FilterOp, values ...post.Status) *commonv1.FilterSpec {
		filter := &commonv1.FilterSpec{Field: "status", Op: op}
		for _, value := range values {
			filter.Values = append(filter.Values, string(value))
		}
		if op == commonv1.FilterOp_FILTER_OP_EQ || op == commonv1.FilterOp_FILTER_OP_NEQ {
			filter.Value, filter.Values = filter.Values[0], nil
		}
		return filter
	}
	inCategory := &commonv1.FilterSpec{Field: "category_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: category}
	bounds := public.MapQuery{West: 126, South: 37, East: 127, North: 38}

	for name, test := range map[string]struct {
		filters []*commonv1.FilterSpec
		want    []string
	}{
		"default, latest published first": {want: []string{elsewhere, published, archived}},
		"published":                       {filters: []*commonv1.FilterSpec{status(commonv1.FilterOp_FILTER_OP_EQ, post.StatusPublished)}, want: []string{elsewhere, published}},
		"either public status":            {filters: []*commonv1.FilterSpec{status(commonv1.FilterOp_FILTER_OP_IN, post.StatusPublished, post.StatusArchived)}, want: []string{elsewhere, published, archived}},
		"not archived":                    {filters: []*commonv1.FilterSpec{status(commonv1.FilterOp_FILTER_OP_NEQ, post.StatusArchived)}, want: []string{elsewhere, published}},
		"archived excluded":               {filters: []*commonv1.FilterSpec{status(commonv1.FilterOp_FILTER_OP_NOT_IN, post.StatusArchived)}, want: []string{elsewhere, published}},
		"in a category":                   {filters: []*commonv1.FilterSpec{inCategory}, want: []string{published, archived}},
	} {
		filters := append([]*commonv1.FilterSpec{search}, test.filters...)
		require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, store public.Store) error {
			listed, total, err := store.ListPosts(ctx, public.ListQuery{Filters: filters, Limit: 10})
			require.NoError(t, err)
			require.EqualValues(t, len(test.want), total, name)
			require.Equal(t, test.want, postIDs(listed), name)
			query := bounds
			query.Filters = filters
			rows, err := store.ListMapFeatures(ctx, query)
			require.NoError(t, err)
			require.ElementsMatch(t, test.want, mapPostIDs(rows), name)
			return nil
		}))
	}

	require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, store public.Store) error {
		found, err := store.SearchPosts(ctx, "%reference%", 10)
		require.NoError(t, err)
		require.Equal(t, []string{elsewhere, published, archived}, postIDs(found), "search never finds a draft")
		for name, test := range map[string]struct {
			query public.MapQuery
			want  int
		}{
			"across the antimeridian": {query: public.MapQuery{West: 126.5, South: 37, East: -170, North: 38}, want: 3},
			"every longitude":         {query: public.MapQuery{West: 0, South: 37, East: 1, North: 38, FullLongitude: true}, want: 3},
			"elsewhere":               {query: public.MapQuery{West: 127, South: 37, East: 128, North: 38}, want: 0},
		} {
			test.query.Filters = []*commonv1.FilterSpec{search}
			rows, err := store.ListMapFeatures(ctx, test.query)
			require.NoError(t, err)
			require.Len(t, rows, test.want, name)
		}
		return nil
	}))
}
