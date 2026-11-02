package referencecatalogpostgres

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// referenceRelation is a join table that points at a reference row.
type referenceRelation struct {
	table  string
	column string
}

// referenceTable is the table of one reference kind, over its model row T.
type referenceTable[T any] struct {
	table   string
	filters *queryutil.FilterConfig
	sorts   *queryutil.SortConfig
	// relations block deletion while they reference the row, and their
	// counts add up to its usage.
	relations []referenceRelation
	// row is the row of value; a kind without a creation time ignores
	// CreatedAt.
	row       func(value referencecatalog.Reference) *T
	reference func(*T) referencecatalog.Reference
}

// referenceRows is a referenceTable for any row type.
type referenceRows interface {
	list(ctx context.Context, db *gorm.DB, page referencecatalog.ListPage) ([]referencecatalog.Reference, int64, error)
	insert(ctx context.Context, db *gorm.DB, value referencecatalog.Reference) (referencecatalog.Reference, error)
	lock(ctx context.Context, db *gorm.DB, id string) (referencecatalog.Reference, error)
	update(ctx context.Context, db *gorm.DB, id string, changes referencecatalog.ReferenceChanges) (referencecatalog.Reference, error)
	delete(ctx context.Context, db *gorm.DB, id string) error
	usedBy() []referenceRelation
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

var referenceTables = map[referencecatalog.ReferenceKind]referenceRows{
	referencecatalog.KindCategory: referenceTable[model.Category]{
		table:   "category",
		filters: nameSearchFilterConfig(),
		sorts: &queryutil.SortConfig{
			AllowedFields: map[string]string{
				"name":       "name",
				"slug":       "slug",
				"created_at": "created_at",
				"updated_at": "updated_at",
			},
			DefaultSort: "name ASC",
		},
		relations: []referenceRelation{
			{table: "post_category", column: "category_id"},
			{table: "release_category", column: "category_id"},
		},
		row: func(value referencecatalog.Reference) *model.Category {
			return &model.Category{ID: value.ID, Name: value.Name, Slug: value.Slug, Description: value.Description, CreatedAt: value.CreatedAt}
		},
		reference: func(row *model.Category) referencecatalog.Reference {
			return referencecatalog.Reference{
				ID: row.ID, Name: row.Name, Slug: row.Slug, Description: row.Description, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
			}
		},
	},
	referencecatalog.KindGenre: referenceTable[model.Genre]{
		table:   "genre",
		filters: nameSearchFilterConfig(),
		sorts: &queryutil.SortConfig{
			AllowedFields: map[string]string{
				"name":       "name",
				"slug":       "slug",
				"created_at": "created_at",
			},
			DefaultSort: "name ASC",
		},
		relations: []referenceRelation{{table: "release_genre", column: "genre_id"}},
		row: func(value referencecatalog.Reference) *model.Genre {
			return &model.Genre{ID: value.ID, Name: value.Name, Slug: value.Slug, Description: value.Description, CreatedAt: value.CreatedAt}
		},
		reference: func(row *model.Genre) referencecatalog.Reference {
			return referencecatalog.Reference{ID: row.ID, Name: row.Name, Slug: row.Slug, Description: row.Description, CreatedAt: row.CreatedAt}
		},
	},
	referencecatalog.KindStyle: referenceTable[model.Style]{
		table:   "style",
		filters: nameSearchFilterConfig(),
		sorts: &queryutil.SortConfig{
			AllowedFields: map[string]string{
				"name":       "name",
				"slug":       "slug",
				"created_at": "created_at",
			},
			DefaultSort: "name ASC",
		},
		relations: []referenceRelation{{table: "release_style", column: "style_id"}},
		row: func(value referencecatalog.Reference) *model.Style {
			return &model.Style{ID: value.ID, Name: value.Name, Slug: value.Slug, Description: value.Description, CreatedAt: value.CreatedAt}
		},
		reference: func(row *model.Style) referencecatalog.Reference {
			return referencecatalog.Reference{ID: row.ID, Name: row.Name, Slug: row.Slug, Description: row.Description, CreatedAt: row.CreatedAt}
		},
	},
	// format has no description or creation time, and its listing order
	// breaks name ties by ID.
	referencecatalog.KindFormat: referenceTable[model.Format]{
		table:   "format",
		filters: nameSearchFilterConfig(),
		sorts: &queryutil.SortConfig{
			AllowedFields: map[string]string{
				"name": "name",
				"slug": "slug",
			},
			DefaultSort: "name ASC, id ASC",
		},
		relations: []referenceRelation{{table: "release_format", column: "format_id"}},
		row: func(value referencecatalog.Reference) *model.Format {
			return &model.Format{ID: value.ID, Name: value.Name, Slug: value.Slug}
		},
		reference: func(row *model.Format) referencecatalog.Reference {
			return referencecatalog.Reference{ID: row.ID, Name: row.Name, Slug: row.Slug}
		},
	},
	referencecatalog.KindTag: referenceTable[model.Tag]{
		table:   "tag",
		filters: nameSearchFilterConfig(),
		sorts: &queryutil.SortConfig{
			AllowedFields: map[string]string{
				"name":       "name",
				"slug":       "slug",
				"created_at": "created_at",
			},
			DefaultSort: "name ASC",
		},
		relations: []referenceRelation{{table: "post_tag", column: "tag_id"}},
		row: func(value referencecatalog.Reference) *model.Tag {
			return &model.Tag{ID: value.ID, Name: value.Name, Slug: value.Slug, CreatedAt: value.CreatedAt}
		},
		reference: func(row *model.Tag) referencecatalog.Reference {
			return referencecatalog.Reference{ID: row.ID, Name: row.Name, Slug: row.Slug, CreatedAt: row.CreatedAt}
		},
	},
}

func tableOf(kind referencecatalog.ReferenceKind) (referenceRows, error) {
	rows, ok := referenceTables[kind]
	if !ok {
		return nil, fmt.Errorf("unknown reference kind %q", kind)
	}
	return rows, nil
}

func (k referenceTable[T]) usedBy() []referenceRelation {
	return k.relations
}

func (k referenceTable[T]) list(ctx context.Context, db *gorm.DB, page referencecatalog.ListPage) ([]referencecatalog.Reference, int64, error) {
	var records []T
	var total int64

	scoped, err := k.filters.ApplyFilters(db.WithContext(ctx).Model(new(T)), page.Filters)
	if err != nil {
		return nil, 0, err
	}
	if err := scoped.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	scoped, err = k.sorts.ApplySort(scoped, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	if err := (queryutil.Pagination{Limit: page.Limit, Offset: page.Offset}).Apply(scoped).Find(&records).Error; err != nil {
		return nil, 0, err
	}
	references := make([]referencecatalog.Reference, len(records))
	for i := range records {
		references[i] = k.reference(&records[i])
	}
	return references, total, nil
}

func (k referenceTable[T]) insert(ctx context.Context, db *gorm.DB, value referencecatalog.Reference) (referencecatalog.Reference, error) {
	value.ID, value.CreatedAt = "", dbtime.Now()
	record := k.row(value)
	if err := db.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(record).Error; err != nil {
		return referencecatalog.Reference{}, referenceTaken(k.table, err)
	}
	return k.reference(record), nil
}

func (k referenceTable[T]) lock(ctx context.Context, db *gorm.DB, id string) (referencecatalog.Reference, error) {
	record := new(T)
	if err := db.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(record, "id = ?", id).Error; err != nil {
		return referencecatalog.Reference{}, notFound(err, referencecatalog.ErrReferenceNotFound)
	}
	return k.reference(record), nil
}

// update stores changes and rereads the row.
func (k referenceTable[T]) update(ctx context.Context, db *gorm.DB, id string, changes referencecatalog.ReferenceChanges) (referencecatalog.Reference, error) {
	updates := structured.Fields{}
	if changes.Name != nil {
		updates["name"] = *changes.Name
	}
	if changes.Slug != nil {
		updates["slug"] = *changes.Slug
	}
	if changes.Description != nil {
		updates["description"] = *changes.Description
	}
	record := k.row(referencecatalog.Reference{ID: id})
	if len(updates) > 0 {
		if err := db.WithContext(ctx).Model(record).Updates(updates).Error; err != nil {
			return referencecatalog.Reference{}, referenceTaken(k.table, err)
		}
	}
	if err := db.WithContext(ctx).First(record, "id = ?", id).Error; err != nil {
		return referencecatalog.Reference{}, notFound(err, referencecatalog.ErrReferenceNotFound)
	}
	return k.reference(record), nil
}

func (k referenceTable[T]) delete(ctx context.Context, db *gorm.DB, id string) error {
	return db.WithContext(ctx).Delete(k.row(referencecatalog.Reference{ID: id})).Error
}

func (s store) ListReferences(ctx context.Context, kind referencecatalog.ReferenceKind, page referencecatalog.ListPage) ([]referencecatalog.Reference, int64, error) {
	rows, err := tableOf(kind)
	if err != nil {
		return nil, 0, err
	}
	return rows.list(ctx, s.tx, page)
}

func (s store) ReferenceUsage(ctx context.Context, kind referencecatalog.ReferenceKind, ids []string) (map[string]int32, error) {
	rows, err := tableOf(kind)
	if err != nil {
		return nil, err
	}
	usage := make(map[string]int32, len(ids))
	for _, relation := range rows.usedBy() {
		counts, err := loadReferenceCounts(ctx, s.tx, relation.table, relation.column, ids)
		if err != nil {
			return nil, err
		}
		for id, count := range counts {
			usage[id] += count
		}
	}
	return usage, nil
}

func (s store) InsertReference(ctx context.Context, kind referencecatalog.ReferenceKind, value referencecatalog.Reference) (referencecatalog.Reference, error) {
	rows, err := tableOf(kind)
	if err != nil {
		return referencecatalog.Reference{}, err
	}
	return rows.insert(ctx, s.tx, value)
}

func (s store) LockReference(ctx context.Context, kind referencecatalog.ReferenceKind, id string) (referencecatalog.Reference, error) {
	rows, err := tableOf(kind)
	if err != nil {
		return referencecatalog.Reference{}, err
	}
	return rows.lock(ctx, s.tx, id)
}

func (s store) UpdateReference(ctx context.Context, kind referencecatalog.ReferenceKind, id string, changes referencecatalog.ReferenceChanges) (referencecatalog.Reference, error) {
	rows, err := tableOf(kind)
	if err != nil {
		return referencecatalog.Reference{}, err
	}
	return rows.update(ctx, s.tx, id, changes)
}

// ReferenceInUse relies on the caller's root row lock: PostgreSQL then
// serializes any foreign-key insertion against that lock.
func (s store) ReferenceInUse(ctx context.Context, kind referencecatalog.ReferenceKind, id string) (bool, error) {
	rows, err := tableOf(kind)
	if err != nil {
		return false, err
	}
	for _, relation := range rows.usedBy() {
		var count int64
		if err := s.tx.WithContext(ctx).Table(relation.table).Where(relation.column+" = ?", id).Limit(1).Count(&count).Error; err != nil {
			return false, err
		}
		if count != 0 {
			return true, nil
		}
	}
	return false, nil
}

func (s store) DeleteReference(ctx context.Context, kind referencecatalog.ReferenceKind, id string) error {
	rows, err := tableOf(kind)
	if err != nil {
		return err
	}
	return rows.delete(ctx, s.tx, id)
}

func loadReferenceCounts(ctx context.Context, db *gorm.DB, table string, column string, ids []string) (map[string]int32, error) {
	counts := make(map[string]int32, len(ids))
	if len(ids) == 0 {
		return counts, nil
	}
	type countRow struct {
		ID    string `gorm:"column:id"`
		Count int64  `gorm:"column:count"`
	}
	var rows []countRow
	if err := db.WithContext(ctx).
		Table(table).
		Select(column+" AS id, COUNT(*) AS count").
		Where(column+" IN ?", ids).
		Group(column).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		counts[row.ID] = int32(row.Count)
	}
	return counts, nil
}
