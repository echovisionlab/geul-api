// Package referencecatalogpublicpostgres is the PostgreSQL store of the
// public catalog reads.
package referencecatalogpublicpostgres

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	referencecatalogpostgres "github.com/echovisionlab/geul-api/internal/referencecatalog/postgres"
	publicreferencecatalog "github.com/echovisionlab/geul-api/internal/referencecatalog/public"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// Assets resolves public assets without exposing file lifecycle mutation
// (referencecatalogadapter.PublicAssets).
type Assets interface {
	ReadyRef(context.Context, *gorm.DB, referencecatalog.AssetSource) *commonv1.AssetRef
}

// NewUnits returns the read snapshots of the public catalog reads.
func NewUnits(db *gorm.DB, assets Assets) (mutation.UnitOfWork[publicreferencecatalog.Store], error) {
	if db == nil || assets == nil {
		return nil, errors.New("public reference catalog store database and assets are required")
	}
	return mutation.Postgres(db, func(tx *gorm.DB) publicreferencecatalog.Store { return store{tx: tx, assets: assets} }), nil
}

type store struct {
	tx     *gorm.DB
	assets Assets
}

func nameSearchFilterConfig() *queryutil.FilterConfig {
	return &queryutil.FilterConfig{
		Fields: map[string]queryutil.FieldDef{
			"search": {
				Type:          queryutil.TypeText,
				AllowedOps:    queryutil.SearchOps,
				SearchColumns: []string{"name"},
			},
		},
	}
}

var (
	categoryFilterConfig = nameSearchFilterConfig()
	clientFilterConfig   = nameSearchFilterConfig()
	tagFilterConfig      = nameSearchFilterConfig()
)

var tagSortConfig = &queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"post_count": "post_count",
	},
	DefaultSort: "name ASC",
}

var categorySortConfig = &queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"post_count": "post_count",
	},
	DefaultSort: "name ASC",
}

var publicClientSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"id":         "id",
		"name":       "name",
		"website":    "website",
		"created_at": "created_at",
	},
	DefaultSort: "name ASC, id ASC",
}

// publicPostStatuses are the Post states whose relations the public lists
// count.
var publicPostStatuses = []string{string(model.PostStatusPublished), string(model.PostStatusArchived)}

func (s store) Categories(ctx context.Context, page referencecatalog.ListPage) ([]publicreferencecatalog.Category, int64, error) {
	type CategoryResult struct {
		ID          string
		Name        string
		Slug        string
		Description *string
		PostCount   int32
	}

	var results []CategoryResult
	var total int64

	// Base query with subquery for category listing
	baseQuery := s.tx.WithContext(ctx).
		Table("category c").
		Select(`
			c.id,
			c.name,
			c.slug,
			c.description,
			COUNT(DISTINCT CASE WHEN p.status IN ? THEN p.id END) as post_count
		`, publicPostStatuses).
		Joins("LEFT JOIN post_category pc ON pc.category_id = c.id").
		Joins("LEFT JOIN post p ON p.id = pc.post_id").
		Group("c.id, c.name, c.slug, c.description")

	// 1. Apply filters (currently none, but for consistency)
	scoped, err := categoryFilterConfig.ApplyFilters(baseQuery, page.Filters)
	if err != nil {
		return nil, 0, err
	}

	// Count total (must use subquery approach for aggregated query)
	countQuery := s.tx.WithContext(ctx).Table("(?) as subq", scoped)
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 2. Apply sort
	scoped, err = categorySortConfig.ApplySort(scoped, page.Sorts)
	if err != nil {
		return nil, 0, err
	}

	// 3. Apply pagination
	scoped = queryutil.ApplyPagination(scoped, queryutil.PaginationParams{Limit: page.Limit, Offset: page.Offset})

	if err := scoped.Scan(&results).Error; err != nil {
		return nil, 0, err
	}
	categories := make([]publicreferencecatalog.Category, 0, len(results))
	for _, r := range results {
		categories = append(categories, publicreferencecatalog.Category{
			ID: r.ID, Name: r.Name, Slug: r.Slug, Description: r.Description, PostCount: r.PostCount,
		})
	}
	return categories, total, nil
}

func (s store) Tags(ctx context.Context, page referencecatalog.ListPage) ([]publicreferencecatalog.Tag, int64, error) {
	type TagResult struct {
		ID        string
		Name      string
		Slug      string
		PostCount int32
	}

	var results []TagResult
	var total int64

	// Base query with subquery for tag listing
	baseQuery := s.tx.WithContext(ctx).
		Table("tag t").
		Select(`
			t.id,
			t.name,
			t.slug,
			COUNT(DISTINCT CASE WHEN p.status IN ? THEN p.id END) as post_count
		`, publicPostStatuses).
		Joins("LEFT JOIN post_tag pt ON pt.tag_id = t.id").
		Joins("LEFT JOIN post p ON p.id = pt.post_id").
		Group("t.id, t.name, t.slug")

	// 1. Apply filters (currently none, but for consistency)
	scoped, err := tagFilterConfig.ApplyFilters(baseQuery, page.Filters)
	if err != nil {
		return nil, 0, err
	}

	// Count total (must use subquery approach for aggregated query)
	countQuery := s.tx.WithContext(ctx).Table("(?) as subq", scoped)
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 2. Apply sort
	scoped, err = tagSortConfig.ApplySort(scoped, page.Sorts)
	if err != nil {
		return nil, 0, err
	}

	// 3. Apply pagination
	scoped = queryutil.ApplyPagination(scoped, queryutil.PaginationParams{Limit: page.Limit, Offset: page.Offset})

	if err := scoped.Scan(&results).Error; err != nil {
		return nil, 0, err
	}
	tags := make([]publicreferencecatalog.Tag, 0, len(results))
	for _, r := range results {
		tags = append(tags, publicreferencecatalog.Tag{ID: r.ID, Name: r.Name, Slug: r.Slug, PostCount: r.PostCount})
	}
	return tags, total, nil
}

func (s store) Client(ctx context.Context, id string) (referencecatalog.Client, error) {
	var client model.Client
	if err := s.tx.WithContext(ctx).First(&client, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return referencecatalog.Client{}, referencecatalog.ErrClientNotFound
		}
		return referencecatalog.Client{}, err
	}
	return clientFromRow(client), nil
}

func (s store) Clients(ctx context.Context, page referencecatalog.ListPage) ([]referencecatalog.Client, int64, error) {
	var rows []model.Client
	var total int64

	scoped, err := clientFilterConfig.ApplyFilters(s.tx.WithContext(ctx).Model(&model.Client{}), page.Filters)
	if err != nil {
		return nil, 0, err
	}
	if err := scoped.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	scoped, err = publicClientSortConfig.ApplySort(scoped, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	if err := queryutil.ApplyPagination(scoped, queryutil.PaginationParams{Limit: page.Limit, Offset: page.Offset}).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	clients := make([]referencecatalog.Client, len(rows))
	for i := range rows {
		clients[i] = clientFromRow(rows[i])
	}
	return clients, total, nil
}

func (s store) MapPlacesByID(ctx context.Context, ids []string) ([]referencecatalog.MapPlace, error) {
	var rows []model.MapPlace
	if err := s.tx.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	places := make([]referencecatalog.MapPlace, len(rows))
	for i := range rows {
		places[i] = referencecatalogpostgres.MapPlaceFromRow(rows[i])
	}
	return places, nil
}

func (s store) ReadyAsset(ctx context.Context, source referencecatalog.AssetSource) *commonv1.AssetRef {
	return s.assets.ReadyRef(ctx, s.tx, source)
}

func clientFromRow(row model.Client) referencecatalog.Client {
	return referencecatalog.Client{
		ID: row.ID, Name: row.Name, Website: row.Website,
		LogoLightFileID: row.LogoLightFileID, LogoDarkFileID: row.LogoDarkFileID, CreatedAt: row.CreatedAt,
	}
}
