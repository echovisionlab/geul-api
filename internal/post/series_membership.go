package post

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// The Series relation of a Post is Post data: the Series aggregate decides
// membership and order, and these functions write the Post rows. Every
// write that changes a Post's series or position advances its updated_at,
// as every other Post change does, so listings ordered by it and the
// sitemap lastmod see the change.

// ErrSeriesRelationChanged reports a Post that left the Series it was read
// in while the caller held no lock on it.
var ErrSeriesRelationChanged = errors.New("post series relation changed")

// SeriesPost is the Post projection a Series lists.
type SeriesPost struct {
	ID           string     `gorm:"column:id"`
	Title        string     `gorm:"column:title"`
	SourceLocale string     `gorm:"column:source_locale"`
	Slug         *string    `gorm:"column:slug"`
	Status       string     `gorm:"column:status"`
	SeriesOrder  *int       `gorm:"column:series_order"`
	PublishedAt  *time.Time `gorm:"column:published_at"`
}

// ListSeriesPosts returns the Posts of seriesID in series order.
func ListSeriesPosts(ctx context.Context, db *gorm.DB, seriesID string) ([]SeriesPost, error) {
	var posts []SeriesPost
	err := db.WithContext(ctx).
		Table("post").
		Select("id, "+PostSourceTitleSQL+" AS title, source_locale, slug, status, series_order, published_at").
		Where("series_id = ?", seriesID).
		Order("series_order ASC NULLS LAST, created_at ASC").
		Find(&posts).Error
	return posts, err
}

// SeriesPostIDs returns the IDs of the Posts of seriesID in series order.
func SeriesPostIDs(ctx context.Context, tx *gorm.DB, seriesID string) ([]string, error) {
	var postIDs []string
	err := tx.WithContext(ctx).Table("post").
		Where("series_id = ?", seriesID).
		Order("series_order ASC, id ASC").
		Pluck("id", &postIDs).Error
	return postIDs, err
}

// ReadSeriesRelation returns the Series of one Post without locking it. It
// reports gorm.ErrRecordNotFound for a missing Post.
func ReadSeriesRelation(ctx context.Context, db *gorm.DB, postID string) (*string, error) {
	return seriesRelation(db.WithContext(ctx), postID)
}

// LockSeriesRelation locks one Post row and returns its Series. It reports
// gorm.ErrRecordNotFound for a missing Post.
func LockSeriesRelation(ctx context.Context, tx *gorm.DB, postID string) (*string, error) {
	return seriesRelation(tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}), postID)
}

func seriesRelation(query *gorm.DB, postID string) (*string, error) {
	var row struct {
		SeriesID *string `gorm:"column:series_id"`
	}
	if err := query.Table("post").Select("series_id").Where("id = ?", postID).Take(&row).Error; err != nil {
		return nil, err
	}
	return row.SeriesID, nil
}

// LockSeriesPosts locks the Post rows of postIDs in ID order and reports how
// many exist.
func LockSeriesPosts(ctx context.Context, tx *gorm.DB, postIDs []string) (int, error) {
	if len(postIDs) == 0 {
		return 0, nil
	}
	ordered := slices.Clone(postIDs)
	slices.Sort(ordered)
	var rows []struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).Table("post").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id").
		Where("id IN ?", ordered).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return 0, err
	}
	return len(rows), nil
}

// NextSeriesOrder is the position after the last Post of seriesID.
func NextSeriesOrder(ctx context.Context, tx *gorm.DB, seriesID string) (int, error) {
	var maxOrder *int
	if err := tx.WithContext(ctx).Table("post").Where("series_id = ?", seriesID).
		Select("MAX(series_order)").Scan(&maxOrder).Error; err != nil {
		return 0, err
	}
	if maxOrder == nil {
		return 0, nil
	}
	return *maxOrder + 1, nil
}

// AssignSeries moves a locked Post into seriesID at order. It reports
// gorm.ErrRecordNotFound for a missing Post.
func AssignSeries(ctx context.Context, tx *gorm.DB, postID, seriesID string, order int, now time.Time) error {
	result := tx.WithContext(ctx).Model(&model.Post{}).Where("id = ?", postID).
		Updates(structured.Fields{"series_id": seriesID, "series_order": order, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ClearSeries removes a locked Post from seriesID. It reports
// ErrSeriesRelationChanged when the Post is not in seriesID.
func ClearSeries(ctx context.Context, tx *gorm.DB, postID, seriesID string, now time.Time) error {
	result := tx.WithContext(ctx).Model(&model.Post{}).
		Where("id = ? AND series_id = ?", postID, seriesID).
		Updates(structured.Fields{"series_id": nil, "series_order": nil, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrSeriesRelationChanged
	}
	return nil
}

// ClearAllSeries removes every Post from a Series being deleted. The caller
// holds the Post locks (LockSeriesPosts).
func ClearAllSeries(ctx context.Context, tx *gorm.DB, seriesID string, now time.Time) error {
	return tx.WithContext(ctx).Model(&model.Post{}).
		Where("series_id = ?", seriesID).
		Updates(structured.Fields{"series_id": nil, "series_order": nil, "updated_at": now}).Error
}

// OrderSeries gives the locked Posts of seriesID the positions of postIDs.
// A Post whose position does not move keeps its updated_at. It reports
// ErrSeriesRelationChanged when a Post is no longer in seriesID.
func OrderSeries(ctx context.Context, tx *gorm.DB, seriesID string, postIDs []string, now time.Time) error {
	for index, postID := range postIDs {
		result := tx.WithContext(ctx).Model(&model.Post{}).
			Where("id = ? AND series_id = ?", postID, seriesID).
			Updates(map[string]any{
				"series_order": index,
				"updated_at":   gorm.Expr("CASE WHEN series_order IS NULL OR series_order <> ? THEN ? ELSE updated_at END", index, now),
			})
		if result.Error != nil {
			return fmt.Errorf("order series post %s: %w", postID, result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrSeriesRelationChanged
		}
	}
	return nil
}
