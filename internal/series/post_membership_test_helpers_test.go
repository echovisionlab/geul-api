package series

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	postdomain "github.com/echovisionlab/geul-api/internal/post"
)

// testPostMembership writes Post membership through the Post-owned
// functions, as the production adapter does; the adapter imports this
// package, so tests here cannot use it.
type testPostMembership struct{}

func (testPostMembership) ListSeriesPosts(ctx context.Context, db *gorm.DB, seriesID string) ([]SeriesPost, error) {
	posts, err := postdomain.ListSeriesPosts(ctx, db, seriesID)
	listed := make([]SeriesPost, len(posts))
	for index, post := range posts {
		listed[index] = SeriesPost(post)
	}
	return listed, err
}

func (testPostMembership) SeriesPostIDs(ctx context.Context, tx *gorm.DB, seriesID string) ([]string, error) {
	return postdomain.SeriesPostIDs(ctx, tx, seriesID)
}

func (testPostMembership) ReadSeriesRelation(ctx context.Context, db *gorm.DB, postID string) (*string, error) {
	return postdomain.ReadSeriesRelation(ctx, db, postID)
}

func (testPostMembership) LockSeriesRelation(ctx context.Context, tx *gorm.DB, postID string) (*string, error) {
	return postdomain.LockSeriesRelation(ctx, tx, postID)
}

func (testPostMembership) LockSeriesPosts(ctx context.Context, tx *gorm.DB, postIDs []string) (int, error) {
	return postdomain.LockSeriesPosts(ctx, tx, postIDs)
}

func (testPostMembership) NextSeriesOrder(ctx context.Context, tx *gorm.DB, seriesID string) (int, error) {
	return postdomain.NextSeriesOrder(ctx, tx, seriesID)
}

func (testPostMembership) AssignSeries(ctx context.Context, tx *gorm.DB, postID, seriesID string, order int, now time.Time) error {
	return postdomain.AssignSeries(ctx, tx, postID, seriesID, order, now)
}

func (testPostMembership) ClearSeries(ctx context.Context, tx *gorm.DB, postID, seriesID string, now time.Time) error {
	return testSeriesRelationError(postdomain.ClearSeries(ctx, tx, postID, seriesID, now))
}

func (testPostMembership) ClearAllSeries(ctx context.Context, tx *gorm.DB, seriesID string, now time.Time) error {
	return postdomain.ClearAllSeries(ctx, tx, seriesID, now)
}

func (testPostMembership) OrderSeries(ctx context.Context, tx *gorm.DB, seriesID string, postIDs []string, now time.Time) error {
	return testSeriesRelationError(postdomain.OrderSeries(ctx, tx, seriesID, postIDs, now))
}

func testSeriesRelationError(err error) error {
	if errors.Is(err, postdomain.ErrSeriesRelationChanged) {
		return ErrPostRelationChanged
	}
	return err
}
