package referencecatalog

import (
	"context"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

type referenceCRUD[T interface{}] struct {
	resource    string
	filters     *queryutil.FilterConfig
	sorts       *queryutil.SortConfig
	newRecord   func(name, slug string, description *string) *T
	values      func(*T) (name, slug string)
	description func(*T) *string
}

func (c referenceCRUD[T]) list(
	ctx context.Context,
	db *gorm.DB,
	filters []*commonv1.FilterSpec,
	sorts []*commonv1.SortSpec,
	request *commonv1.PaginationRequest,
) ([]T, int64, queryutil.Pagination, error) {
	var records []T
	var total int64

	query, err := c.filters.ApplyFilters(db.WithContext(ctx).Model(new(T)), filters)
	if err != nil {
		return nil, 0, queryutil.Pagination{}, err
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, queryutil.Pagination{}, errs.Internal(err)
	}
	query, err = c.sorts.ApplySort(query, sorts)
	if err != nil {
		return nil, 0, queryutil.Pagination{}, err
	}

	page := referencePagination(request)
	if err := page.Apply(query).Find(&records).Error; err != nil {
		return nil, 0, queryutil.Pagination{}, errs.Internal(err)
	}
	return records, total, page, nil
}

func (c referenceCRUD[T]) create(
	ctx context.Context,
	db *gorm.DB,
	name string,
	slug *string,
	description *string,
) (*T, error) {
	name, err := normalizeReferenceName(name)
	if err != nil {
		return nil, err
	}

	normalizedSlug := strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(name))
	if slug != nil {
		normalizedSlug = *slug
	}
	normalizedSlug, err = normalizeReferenceSlug(normalizedSlug)
	if err != nil {
		return nil, err
	}

	record := c.newRecord(name, normalizedSlug, description)
	if err := db.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(record).Error; err != nil {
		return nil, classifyReferenceUniqueViolation(c.resource, name, normalizedSlug, err)
	}
	return record, nil
}

func (c referenceCRUD[T]) lockForMutation(
	ctx context.Context,
	db *gorm.DB,
	id string,
) (*T, error) {
	record := new(T)
	if err := db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(record, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound(c.resource, id)
		}
		return nil, errs.Internal(err)
	}
	return record, nil
}

func (c referenceCRUD[T]) updateLocked(
	ctx context.Context,
	db *gorm.DB,
	id string,
	record *T,
	name *string,
	slug *string,
	description *string,
) (*T, []string, error) {
	if record == nil {
		return nil, nil, errs.InternalMsg(c.resource + " mutation requires a locked row")
	}

	nextName, nextSlug := c.values(record)
	updates := structured.Fields{}
	changedFields := make([]string, 0, 3)
	if name != nil {
		nextName, err := normalizeReferenceName(*name)
		if err != nil {
			return nil, nil, err
		}
		currentName, _ := c.values(record)
		if nextName != currentName {
			updates["name"] = nextName
			changedFields = append(changedFields, "name")
		}
	}
	if slug != nil {
		nextSlug, err := normalizeReferenceSlug(*slug)
		if err != nil {
			return nil, nil, err
		}
		_, currentSlug := c.values(record)
		if nextSlug != currentSlug {
			updates["slug"] = nextSlug
			changedFields = append(changedFields, "slug")
		}
	}
	if description != nil {
		if c.description == nil {
			return nil, nil, errs.InvalidArgument("description", "is not supported")
		}
		currentDescription := c.description(record)
		if currentDescription == nil || *currentDescription != *description {
			updates["description"] = *description
			changedFields = append(changedFields, "description")
		}
	}

	if len(updates) > 0 {
		if err := db.WithContext(ctx).Model(record).Updates(updates).Error; err != nil {
			return nil, nil, classifyReferenceUniqueViolation(c.resource, nextName, nextSlug, err)
		}
	}
	if err := db.WithContext(ctx).First(record, "id = ?", id).Error; err != nil {
		return nil, nil, errs.Internal(err)
	}
	return record, changedFields, nil
}

type referenceRelationGuard struct {
	table  string
	column string
}

// deleteLockedWithRelationGuards relies on the caller's root row lock.
// PostgreSQL then serializes any FK insertion against that lock, so the count
// and delete are one authority boundary rather than a stale UI-derived decision.
func (c referenceCRUD[T]) deleteLockedWithRelationGuards(
	ctx context.Context,
	db *gorm.DB,
	id string,
	record *T,
	guards ...referenceRelationGuard,
) error {
	if record == nil {
		return errs.InternalMsg(c.resource + " deletion requires a locked row")
	}
	for _, guard := range guards {
		var count int64
		if err := db.WithContext(ctx).Table(guard.table).Where(guard.column+" = ?", id).Limit(1).Count(&count).Error; err != nil {
			return errs.Internal(err)
		}
		if count != 0 {
			return errs.FailedPrecondition(c.resource + " is still referenced")
		}
	}
	if err := db.WithContext(ctx).Delete(record).Error; err != nil {
		return errs.Internal(err)
	}
	return nil
}

func referencePagination(request *commonv1.PaginationRequest) queryutil.Pagination {
	page := queryutil.Pagination{Limit: 100}
	if request == nil {
		return page
	}
	if request.Limit > 0 {
		page.Limit = request.Limit
	}
	page.Offset = request.Offset
	return page
}

func loadReferenceCounts(
	ctx context.Context,
	db *gorm.DB,
	table string,
	column string,
	ids []string,
) (map[string]int32, error) {
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
		return nil, errs.Internal(err)
	}
	for _, row := range rows {
		counts[row.ID] = int32(row.Count)
	}
	return counts, nil
}
