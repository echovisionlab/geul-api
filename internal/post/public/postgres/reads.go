package postpublicpostgres

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/post"
	postpostgres "github.com/echovisionlab/geul-api/internal/post/postgres"
	"github.com/echovisionlab/geul-api/internal/post/public"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func publicPostStatusValues() []string {
	return []string{
		string(model.PostStatusPublished),
		string(model.PostStatusArchived),
	}
}

func withPublicPostStatusFence(query *gorm.DB) *gorm.DB {
	return query.Where("post.status IN ?", publicPostStatusValues())
}

func postsFromRows(rows []model.Post) []post.Post {
	posts := make([]post.Post, len(rows))
	for index, row := range rows {
		posts[index] = postpostgres.PostFromRow(row)
	}
	return posts
}

func (s *store) ListPosts(ctx context.Context, request public.ListQuery) ([]post.Post, int64, error) {
	var rows []model.Post
	var total int64

	query := withPublicPostStatusFence(s.tx.WithContext(ctx).Model(&model.Post{}).
		Preload("Categories").
		Preload("Tags").
		Preload("FeaturedImage"))

	query, err := applyPublicPostFilters(query, request.Filters)
	if err != nil {
		return nil, 0, err
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	query, err = PostSortConfig.ApplySort(query, request.Sorts)
	if err != nil {
		return nil, 0, err
	}
	query = query.Limit(int(request.Limit)).Offset(int(request.Offset))
	if err := query.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return postsFromRows(rows), total, nil
}

func invalidPostFilterOp(filter *commonv1.FilterSpec) error {
	return domainerr.New(domainerr.Invalid, filter.GetField(),
		fmt.Sprintf("invalid filter operation '%s' for field '%s'", filter.GetOp().String(), filter.GetField()))
}

func applyPublicPostFilters(
	query *gorm.DB,
	filters []*commonv1.FilterSpec,
) (*gorm.DB, error) {
	var remainingFilters []*commonv1.FilterSpec

	for _, f := range filters {
		switch f.GetField() {
		case "category_id":
			if f.GetOp() == commonv1.FilterOp_FILTER_OP_EQ {
				query = query.Where("post.id IN (SELECT post_id FROM post_category WHERE category_id = ?)", f.GetValue())
			} else if f.GetOp() == commonv1.FilterOp_FILTER_OP_IN {
				query = query.Where("post.id IN (SELECT post_id FROM post_category WHERE category_id IN ?)", f.GetValues())
			} else {
				return nil, invalidPostFilterOp(f)
			}
		case "tag_id":
			if f.GetOp() == commonv1.FilterOp_FILTER_OP_EQ {
				query = query.Where("post.id IN (SELECT post_id FROM post_tag WHERE tag_id = ?)", f.GetValue())
			} else if f.GetOp() == commonv1.FilterOp_FILTER_OP_IN {
				query = query.Where("post.id IN (SELECT post_id FROM post_tag WHERE tag_id IN ?)", f.GetValues())
			} else {
				return nil, invalidPostFilterOp(f)
			}
		case "author_id":
			if f.GetOp() == commonv1.FilterOp_FILTER_OP_EQ {
				query = query.Where(
					"post.id IN (SELECT pa.post_id FROM post_author AS pa WHERE pa.member_id = ?)",
					f.GetValue(),
				)
			} else if f.GetOp() == commonv1.FilterOp_FILTER_OP_IN {
				authorIDs := f.GetValues()
				if len(authorIDs) == 0 {
					query = query.Where("1 = 0")
					continue
				}
				query = query.Where(
					"post.id IN (SELECT DISTINCT pa.post_id FROM post_author AS pa WHERE pa.member_id IN ?)",
					authorIDs,
				)
			} else {
				return nil, invalidPostFilterOp(f)
			}
		default:
			remainingFilters = append(remainingFilters, f)
		}
	}

	return PostFilterConfig.ApplyFilters(query, remainingFilters)
}

func (s *store) ListMapFeatures(ctx context.Context, request public.MapQuery) ([]public.MapFeatureRow, error) {
	var rows []struct {
		PostID       string  `gorm:"column:post_id"`
		PostTitle    string  `gorm:"column:post_title"`
		PostSlug     *string `gorm:"column:post_slug"`
		PlaceID      string  `gorm:"column:place_id"`
		PlaceName    string  `gorm:"column:place_name"`
		PlaceAddress string  `gorm:"column:place_address"`
		PlaceLat     float64 `gorm:"column:place_lat"`
		PlaceLng     float64 `gorm:"column:place_lng"`
	}
	query := withPublicPostStatusFence(s.tx.WithContext(ctx).
		Model(&model.Post{}).
		Select(`
			post.id AS post_id,
			`+postpostgres.PostSourceTitleSQL+` AS post_title,
			post.slug AS post_slug,
			map_place.id AS place_id,
			map_place.name AS place_name,
			map_place.address AS place_address,
			map_place.lat AS place_lat,
			map_place.lng AS place_lng
		`).
		Joins("JOIN map_place ON map_place.id = post.map_place_id").
		Where("map_place.lat BETWEEN ? AND ?", request.South, request.North))

	switch {
	case request.FullLongitude:
		// World-wrapping viewport: longitude filter would incorrectly exclude visible places.
	case request.West <= request.East:
		query = query.Where("map_place.lng BETWEEN ? AND ?", request.West, request.East)
	default:
		query = query.Where("(map_place.lng >= ? OR map_place.lng <= ?)", request.West, request.East)
	}

	query, err := applyPublicPostFilters(query, request.Filters)
	if err != nil {
		return nil, err
	}
	query, err = PostSortConfig.ApplySort(query, request.Sorts)
	if err != nil {
		return nil, err
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	features := make([]public.MapFeatureRow, len(rows))
	for index, row := range rows {
		features[index] = public.MapFeatureRow(row)
	}
	return features, nil
}

func (s *store) SearchPosts(ctx context.Context, pattern string, limit int) ([]post.Post, error) {
	var rows []model.Post
	if err := withPublicPostStatusFence(s.tx.WithContext(ctx).
		Model(&model.Post{}).
		Where(postpostgres.PostSourceTitleSQL+" ILIKE ? OR "+postpostgres.PostSourceContentTextSQL+" ILIKE ?", pattern, pattern).
		Preload("Categories").
		Preload("Tags").
		Preload("FeaturedImage").
		Order("published_at DESC NULLS LAST").
		Limit(limit)).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return postsFromRows(rows), nil
}
