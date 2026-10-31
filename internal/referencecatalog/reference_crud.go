package referencecatalog

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// referencePolicy is the SpiceDB contract of an admin-managed reference kind,
// such as policyv1.Genre.
type referencePolicy interface {
	List() (policyv1.Can, error)
	Create() (policyv1.Can, error)
	Edit(id string) (policyv1.Can, error)
	Delete(id string) (policyv1.Can, error)
	TouchPolicy(id string) (policyv1.RelationshipMutation, error)
	DeletePolicy(id string) (policyv1.RelationshipMutation, error)
}

type referenceAudit struct {
	created    sharedtelemetry.AuditAction
	updated    sharedtelemetry.AuditAction
	deleted    sharedtelemetry.AuditAction
	newCreated func(sharedtelemetry.AuditMetadata, string) (sharedtelemetry.AuditRecord, error)
	newUpdated func(sharedtelemetry.AuditMetadata, string, []string) (sharedtelemetry.AuditRecord, error)
	newDeleted func(sharedtelemetry.AuditMetadata, string) (sharedtelemetry.AuditRecord, error)
}

// referenceRelation is a join table that points at a reference row.
type referenceRelation struct {
	table  string
	column string
}

// referenceKind describes one reference catalog kind. The kind files hold the
// descriptor, the Connect surface and the proto mapping; the list, create,
// update and delete flows below are shared.
type referenceKind[T any] struct {
	resource  string
	maxLength int
	filters   *queryutil.FilterConfig
	sorts     *queryutil.SortConfig
	policy    referencePolicy
	audit     referenceAudit
	// relations block deletion while they reference the row, and their counts
	// add up to the usage shown in the admin list.
	relations []referenceRelation
	// menuLinkType is set for kinds that menu items can link to; slug changes
	// and deletions then update those menu targets in the same transaction.
	menuLinkType string
	newRecord    func(name, slug string, description *string) *T
	values       func(*T) (id, name, slug string)
	// description is nil for kinds without a description column.
	description func(*T) *string
}

// referenceService holds what every reference kind service needs.
type referenceService struct {
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	auditWriter domainaudit.Appender
	menuTargets MenuTargets
}

func newReferenceService(db *gorm.DB, spiceDB *auth.SpiceDBClient) referenceService {
	if db == nil {
		panic("db is required")
	}
	if spiceDB == nil {
		panic("spiceDB is required")
	}
	return referenceService{db: db, spiceDB: spiceDB}
}

func newMenuTargetReferenceService(db *gorm.DB, menuTargets MenuTargets, spiceDB *auth.SpiceDBClient) referenceService {
	service := newReferenceService(db, spiceDB)
	if menuTargets == nil {
		panic("menu targets are required")
	}
	service.menuTargets = menuTargets
	return service
}

func requireReferenceAuditWriter(resource string, auditWriter domainaudit.Appender) {
	if auditWriter == nil {
		panic(resource + " audit writer is required")
	}
}

func mapReferences[T, P any](records []T, project func(*T) P) []P {
	result := make([]P, len(records))
	for i := range records {
		result[i] = project(&records[i])
	}
	return result
}

func (k referenceKind[T]) list(
	ctx context.Context,
	db *gorm.DB,
	filters []*commonv1.FilterSpec,
	sorts []*commonv1.SortSpec,
	request *commonv1.PaginationRequest,
) ([]T, int64, queryutil.Pagination, error) {
	var records []T
	var total int64

	query, err := k.filters.ApplyFilters(db.WithContext(ctx).Model(new(T)), filters)
	if err != nil {
		return nil, 0, queryutil.Pagination{}, err
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, queryutil.Pagination{}, errs.Internal(err)
	}
	query, err = k.sorts.ApplySort(query, sorts)
	if err != nil {
		return nil, 0, queryutil.Pagination{}, err
	}

	page := referencePagination(request)
	if err := page.Apply(query).Find(&records).Error; err != nil {
		return nil, 0, queryutil.Pagination{}, errs.Internal(err)
	}
	return records, total, page, nil
}

// listAdmin lists the kind for administrators with each row's usage count.
func (k referenceKind[T]) listAdmin(
	ctx context.Context,
	service referenceService,
	filters []*commonv1.FilterSpec,
	sorts []*commonv1.SortSpec,
	request *commonv1.PaginationRequest,
) ([]T, map[string]int32, int64, queryutil.Pagination, error) {
	can, err := k.policy.List()
	if err != nil {
		return nil, nil, 0, queryutil.Pagination{}, errs.Internal(err)
	}
	if err := authz.RequireAdminCan(ctx, service.spiceDB, can); err != nil {
		return nil, nil, 0, queryutil.Pagination{}, err
	}
	records, total, page, err := k.list(ctx, service.db, filters, sorts, request)
	if err != nil {
		return nil, nil, 0, queryutil.Pagination{}, err
	}
	ids := make([]string, len(records))
	for i := range records {
		ids[i], _, _ = k.values(&records[i])
	}
	usage := make(map[string]int32, len(ids))
	for _, relation := range k.relations {
		counts, err := loadReferenceCounts(ctx, service.db, relation.table, relation.column, ids)
		if err != nil {
			return nil, nil, 0, queryutil.Pagination{}, err
		}
		for id, count := range counts {
			usage[id] += count
		}
	}
	return records, usage, total, page, nil
}

func (k referenceKind[T]) createAudited(
	ctx context.Context,
	service referenceService,
	name string,
	slug *string,
	description *string,
) (*T, error) {
	can, err := k.policy.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	var record *T
	_, err = authzmutation.Execute(ctx, service.db, service.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, service.spiceDB, can); err != nil {
			return err
		}
		var err error
		record, err = k.create(ctx, tx, name, slug, description)
		if err != nil {
			return err
		}
		id, _, _ := k.values(record)
		if err := domainaudit.AppendOptionalRequest(ctx, tx, service.auditWriter, k.audit.created, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return k.audit.newCreated(metadata, id)
		}); err != nil {
			return err
		}
		policyTouch, err := k.policy.TouchPolicy(id)
		if err != nil {
			return err
		}
		policyDelete, err := k.policy.DeletePolicy(id)
		if err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{policyTouch}, []policyv1.RelationshipMutation{policyDelete})
	})
	if err != nil {
		return nil, err
	}
	return record, nil
}

func (k referenceKind[T]) updateAudited(
	ctx context.Context,
	service referenceService,
	id string,
	name *string,
	slug *string,
	description *string,
) (_ *T, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String(k.resource+"_id", id))

	can, err := k.policy.Edit(id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	var record *T
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		record, err = k.lockForMutation(ctx, tx, id)
		if err != nil {
			return err
		}
		_, _, previousSlug := k.values(record)
		if err := identitystate.RequireFreshAdminCan(ctx, tx, service.spiceDB, can); err != nil {
			return err
		}
		var changedFields []string
		record, changedFields, err = k.updateLocked(ctx, tx, id, record, name, slug, description)
		if err != nil || len(changedFields) == 0 {
			return err
		}
		recordID, _, nextSlug := k.values(record)
		if k.menuLinkType != "" && previousSlug != nextSlug {
			if err := service.menuTargets.UpdateSlug(ctx, tx, MenuTargetSlugChange{
				Target:   MenuTarget{LinkType: k.menuLinkType, ID: recordID, Slug: previousSlug},
				NextSlug: nextSlug,
			}); err != nil {
				return err
			}
		}
		return domainaudit.AppendOptionalRequest(ctx, tx, service.auditWriter, k.audit.updated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return k.audit.newUpdated(metadata, recordID, changedFields)
		})
	})
	if err != nil {
		return nil, err
	}
	return record, nil
}

func (k referenceKind[T]) deleteAudited(ctx context.Context, service referenceService, id string) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String(k.resource+"_id", id))

	can, err := k.policy.Delete(id)
	if err != nil {
		return errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return err
	}
	_, err = authzmutation.Execute(ctx, service.db, service.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		record, err := k.lockForMutation(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, service.spiceDB, can); err != nil {
			return err
		}
		if err := k.deleteLocked(ctx, tx, id, record); err != nil {
			return err
		}
		recordID, _, recordSlug := k.values(record)
		if k.menuLinkType != "" {
			if err := service.menuTargets.Remove(ctx, tx, MenuTarget{LinkType: k.menuLinkType, ID: recordID, Slug: recordSlug}); err != nil {
				return err
			}
		}
		if err := domainaudit.AppendOptionalRequest(ctx, tx, service.auditWriter, k.audit.deleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return k.audit.newDeleted(metadata, recordID)
		}); err != nil {
			return err
		}
		policyDelete, err := k.policy.DeletePolicy(recordID)
		if err != nil {
			return err
		}
		policyTouch, err := k.policy.TouchPolicy(recordID)
		if err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{policyDelete}, []policyv1.RelationshipMutation{policyTouch})
	})
	return err
}

func (k referenceKind[T]) create(
	ctx context.Context,
	db *gorm.DB,
	name string,
	slug *string,
	description *string,
) (*T, error) {
	name, err := normalizeReferenceName(name, k.maxLength)
	if err != nil {
		return nil, err
	}

	normalizedSlug := strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(name))
	if slug != nil {
		normalizedSlug = *slug
	}
	normalizedSlug, err = normalizeReferenceSlug(normalizedSlug, k.maxLength)
	if err != nil {
		return nil, err
	}

	record := k.newRecord(name, normalizedSlug, description)
	if err := db.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(record).Error; err != nil {
		return nil, classifyReferenceUniqueViolation(k.resource, name, normalizedSlug, err)
	}
	return record, nil
}

func (k referenceKind[T]) lockForMutation(
	ctx context.Context,
	db *gorm.DB,
	id string,
) (*T, error) {
	record := new(T)
	if err := db.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(record, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound(k.resource, id)
		}
		return nil, errs.Internal(err)
	}
	return record, nil
}

func (k referenceKind[T]) updateLocked(
	ctx context.Context,
	db *gorm.DB,
	id string,
	record *T,
	name *string,
	slug *string,
	description *string,
) (*T, []string, error) {
	if record == nil {
		return nil, nil, errs.InternalMsg(k.resource + " mutation requires a locked row")
	}

	_, currentName, currentSlug := k.values(record)
	nextName, nextSlug := currentName, currentSlug
	updates := structured.Fields{}
	changedFields := make([]string, 0, 3)
	var err error
	if name != nil {
		if nextName, err = normalizeReferenceName(*name, k.maxLength); err != nil {
			return nil, nil, err
		}
		if nextName != currentName {
			updates["name"] = nextName
			changedFields = append(changedFields, "name")
		}
	}
	if slug != nil {
		if nextSlug, err = normalizeReferenceSlug(*slug, k.maxLength); err != nil {
			return nil, nil, err
		}
		if nextSlug != currentSlug {
			updates["slug"] = nextSlug
			changedFields = append(changedFields, "slug")
		}
	}
	if description != nil {
		if k.description == nil {
			return nil, nil, errs.InvalidArgument("description", "is not supported")
		}
		currentDescription := k.description(record)
		if currentDescription == nil || *currentDescription != *description {
			updates["description"] = *description
			changedFields = append(changedFields, "description")
		}
	}

	if len(updates) > 0 {
		if err := db.WithContext(ctx).Model(record).Updates(updates).Error; err != nil {
			return nil, nil, classifyReferenceUniqueViolation(k.resource, nextName, nextSlug, err)
		}
	}
	if err := db.WithContext(ctx).First(record, "id = ?", id).Error; err != nil {
		return nil, nil, errs.Internal(err)
	}
	return record, changedFields, nil
}

// deleteLocked relies on the caller's root row lock. PostgreSQL then
// serializes any FK insertion against that lock, so the count and delete are
// one authority boundary rather than a stale UI-derived decision.
func (k referenceKind[T]) deleteLocked(
	ctx context.Context,
	db *gorm.DB,
	id string,
	record *T,
) error {
	if record == nil {
		return errs.InternalMsg(k.resource + " deletion requires a locked row")
	}
	for _, relation := range k.relations {
		var count int64
		if err := db.WithContext(ctx).Table(relation.table).Where(relation.column+" = ?", id).Limit(1).Count(&count).Error; err != nil {
			return errs.Internal(err)
		}
		if count != 0 {
			return errs.FailedPrecondition(k.resource + " is still referenced")
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
