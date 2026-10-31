package post

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	postdomain "github.com/echovisionlab/geul-api/internal/post"
	seriesdomain "github.com/echovisionlab/geul-api/internal/series"
)

// SeriesMembership adapts the Post-owned Series relation functions to the
// Series membership port.
type SeriesMembership struct{}

func (SeriesMembership) ListSeriesPosts(ctx context.Context, db *gorm.DB, seriesID string) ([]seriesdomain.SeriesPost, error) {
	posts, err := postdomain.ListSeriesPosts(ctx, db, seriesID)
	if err != nil {
		return nil, err
	}
	listed := make([]seriesdomain.SeriesPost, len(posts))
	for index, post := range posts {
		listed[index] = seriesdomain.SeriesPost(post)
	}
	return listed, nil
}

func (SeriesMembership) SeriesPostIDs(ctx context.Context, tx *gorm.DB, seriesID string) ([]string, error) {
	return postdomain.SeriesPostIDs(ctx, tx, seriesID)
}

func (SeriesMembership) ReadSeriesRelation(ctx context.Context, db *gorm.DB, postID string) (*string, error) {
	return postdomain.ReadSeriesRelation(ctx, db, postID)
}

func (SeriesMembership) LockSeriesRelation(ctx context.Context, tx *gorm.DB, postID string) (*string, error) {
	return postdomain.LockSeriesRelation(ctx, tx, postID)
}

func (SeriesMembership) LockSeriesPosts(ctx context.Context, tx *gorm.DB, postIDs []string) (int, error) {
	return postdomain.LockSeriesPosts(ctx, tx, postIDs)
}

func (SeriesMembership) NextSeriesOrder(ctx context.Context, tx *gorm.DB, seriesID string) (int, error) {
	return postdomain.NextSeriesOrder(ctx, tx, seriesID)
}

func (SeriesMembership) AssignSeries(ctx context.Context, tx *gorm.DB, postID, seriesID string, order int, now time.Time) error {
	return postdomain.AssignSeries(ctx, tx, postID, seriesID, order, now)
}

func (SeriesMembership) ClearSeries(ctx context.Context, tx *gorm.DB, postID, seriesID string, now time.Time) error {
	return seriesRelationError(postdomain.ClearSeries(ctx, tx, postID, seriesID, now))
}

func (SeriesMembership) ClearAllSeries(ctx context.Context, tx *gorm.DB, seriesID string, now time.Time) error {
	return postdomain.ClearAllSeries(ctx, tx, seriesID, now)
}

func (SeriesMembership) OrderSeries(ctx context.Context, tx *gorm.DB, seriesID string, postIDs []string, now time.Time) error {
	return seriesRelationError(postdomain.OrderSeries(ctx, tx, seriesID, postIDs, now))
}

func seriesRelationError(err error) error {
	if errors.Is(err, postdomain.ErrSeriesRelationChanged) {
		return seriesdomain.ErrPostRelationChanged
	}
	return err
}
