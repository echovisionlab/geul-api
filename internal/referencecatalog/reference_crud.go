package referencecatalog

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// ReferenceListQuery pages one reference kind. The Genre, Style, Format, Tag
// and Category lists share it.
type ReferenceListQuery struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	// Limit is 100 when not positive.
	Limit  int32
	Offset int32
}

// CreateReferenceCommand creates a reference. Slug derives from Name when
// nil; Description is ignored by kinds without one.
type CreateReferenceCommand struct {
	Name        string
	Slug        *string
	Description *string
}

// UpdateReferenceCommand changes the supplied fields of reference ID.
type UpdateReferenceCommand struct {
	ID          string
	Name        *string
	Slug        *string
	Description *string
}

// DeleteReferenceCommand deletes reference ID when nothing references it.
type DeleteReferenceCommand struct {
	ID string
}

// referencePolicy is the SpiceDB contract of an admin-managed reference kind,
// such as policyv1.Genre.
type referencePolicy interface {
	List() (policyv1.Can, error)
	Create() (policyv1.Can, error)
	Edit(id string) (policyv1.Can, error)
	Delete(id string) (policyv1.Can, error)
	policyRelations
}

// policyRelations writes the policy relationship of a catalog row.
type policyRelations interface {
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
// descriptor, the use cases and the proto mapping; the list, create, update
// and delete flows below are shared.
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

func mapReferences[T, P any](records []T, project func(*T) P) []P {
	result := make([]P, len(records))
	for i := range records {
		result[i] = project(&records[i])
	}
	return result
}

// projectReference maps the result of a create or update.
func projectReference[T, P any](record *T, err error, project func(*T) P) (P, error) {
	if err != nil {
		var zero P
		return zero, err
	}
	return project(record), nil
}

func (k referenceKind[T]) list(ctx context.Context, db *gorm.DB, query ReferenceListQuery) ([]T, *commonv1.PaginationResponse, error) {
	var records []T
	var total int64

	scoped, err := k.filters.ApplyFilters(db.WithContext(ctx).Model(new(T)), query.Filters)
	if err != nil {
		return nil, nil, err
	}
	if err := scoped.Count(&total).Error; err != nil {
		return nil, nil, err
	}
	scoped, err = k.sorts.ApplySort(scoped, query.Sorts)
	if err != nil {
		return nil, nil, err
	}

	page := queryutil.Pagination{Limit: 100, Offset: query.Offset}
	if query.Limit > 0 {
		page.Limit = query.Limit
	}
	if err := page.Apply(scoped).Find(&records).Error; err != nil {
		return nil, nil, err
	}
	return records, page.BuildResponse(total), nil
}

func listReferences[T, P any](ctx context.Context, s *Service, kind referenceKind[T], query ReferenceListQuery, project func(*T) P) (Page[P], error) {
	records, pagination, err := kind.list(ctx, s.db, query)
	if err != nil {
		return Page[P]{}, err
	}
	return Page[P]{Items: mapReferences(records, project), Pagination: pagination}, nil
}

// listReferencesAdmin lists the kind for administrators with each row's usage
// count.
func listReferencesAdmin[T, P any](
	ctx context.Context, s *Service, kind referenceKind[T], query ReferenceListQuery, project func(record *T, usage int32) P,
) (Page[P], error) {
	can, err := kind.policy.List()
	if err != nil {
		return Page[P]{}, err
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return Page[P]{}, err
	}
	records, pagination, err := kind.list(ctx, s.db, query)
	if err != nil {
		return Page[P]{}, err
	}
	ids := make([]string, len(records))
	for i := range records {
		ids[i], _, _ = kind.values(&records[i])
	}
	usage := make(map[string]int32, len(ids))
	for _, relation := range kind.relations {
		counts, err := loadReferenceCounts(ctx, s.db, relation.table, relation.column, ids)
		if err != nil {
			return Page[P]{}, err
		}
		for id, count := range counts {
			usage[id] += count
		}
	}
	items := make([]P, len(records))
	for i := range records {
		items[i] = project(&records[i], usage[ids[i]])
	}
	return Page[P]{Items: items, Pagination: pagination}, nil
}

func (k referenceKind[T]) create(ctx context.Context, s *Service, command CreateReferenceCommand) (*T, error) {
	can, err := k.policy.Create()
	if err != nil {
		return nil, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	return mutation.Run(ctx, s.mutations, mutation.Spec[*T]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, tx *gorm.DB) (*T, error) {
			return k.insert(ctx, tx, command)
		},
		Audit: func(record *T) mutation.Audit {
			id, _, _ := k.values(record)
			return mutation.RequestAudit(k.audit.created, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return k.audit.newCreated(metadata, id)
			})
		},
		Relationships: func(record *T) (apply, compensate []policyv1.RelationshipMutation, err error) {
			id, _, _ := k.values(record)
			return referencePolicyRelationships(k.policy, id, true)
		},
	})
}

type updatedReference[T any] struct {
	record  *T
	changed []string
}

func (k referenceKind[T]) update(ctx context.Context, s *Service, command UpdateReferenceCommand) (_ *T, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String(k.resource+"_id", command.ID))

	can, err := k.policy.Edit(command.ID)
	if err != nil {
		return nil, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	var record *T
	result, err := mutation.Run(ctx, s.mutations, mutation.Spec[updatedReference[T]]{
		Lock: func(ctx context.Context, tx *gorm.DB) (err error) {
			record, err = k.lockForMutation(ctx, tx, command.ID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, tx *gorm.DB) (updatedReference[T], error) {
			_, _, previousSlug := k.values(record)
			changed, err := k.updateLocked(ctx, tx, record, command)
			if err != nil || len(changed) == 0 {
				return updatedReference[T]{record: record}, err
			}
			recordID, _, nextSlug := k.values(record)
			if k.menuLinkType != "" && previousSlug != nextSlug {
				if err := s.menuTargets.UpdateSlug(ctx, tx, MenuTargetSlugChange{
					Target:   MenuTarget{LinkType: k.menuLinkType, ID: recordID, Slug: previousSlug},
					NextSlug: nextSlug,
				}); err != nil {
					return updatedReference[T]{}, err
				}
			}
			return updatedReference[T]{record: record, changed: changed}, nil
		},
		Audit: func(result updatedReference[T]) mutation.Audit {
			if len(result.changed) == 0 {
				return mutation.Unchanged()
			}
			recordID, _, _ := k.values(result.record)
			return mutation.RequestAudit(k.audit.updated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return k.audit.newUpdated(metadata, recordID, result.changed)
			})
		},
	})
	if err != nil {
		return nil, err
	}
	return result.record, nil
}

func (k referenceKind[T]) delete(ctx context.Context, s *Service, command DeleteReferenceCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String(k.resource+"_id", command.ID))

	can, err := k.policy.Delete(command.ID)
	if err != nil {
		return err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return err
	}
	var record *T
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Lock: func(ctx context.Context, tx *gorm.DB) (err error) {
			record, err = k.lockForMutation(ctx, tx, command.ID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, tx *gorm.DB) (string, error) {
			if err := k.deleteLocked(ctx, tx, command.ID, record); err != nil {
				return "", err
			}
			recordID, _, recordSlug := k.values(record)
			if k.menuLinkType != "" {
				if err := s.menuTargets.Remove(ctx, tx, MenuTarget{LinkType: k.menuLinkType, ID: recordID, Slug: recordSlug}); err != nil {
					return "", err
				}
			}
			return recordID, nil
		},
		Audit: func(recordID string) mutation.Audit {
			return mutation.RequestAudit(k.audit.deleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return k.audit.newDeleted(metadata, recordID)
			})
		},
		Relationships: func(recordID string) (apply, compensate []policyv1.RelationshipMutation, err error) {
			return referencePolicyRelationships(k.policy, recordID, false)
		},
	})
	return err
}

// referencePolicyRelationships is the policy write of a created (touch) or
// deleted catalog row, and its exact inverse.
func referencePolicyRelationships(policy policyRelations, id string, created bool) (apply, compensate []policyv1.RelationshipMutation, err error) {
	touch, err := policy.TouchPolicy(id)
	if err != nil {
		return nil, nil, err
	}
	remove, err := policy.DeletePolicy(id)
	if err != nil {
		return nil, nil, err
	}
	if created {
		return []policyv1.RelationshipMutation{touch}, []policyv1.RelationshipMutation{remove}, nil
	}
	return []policyv1.RelationshipMutation{remove}, []policyv1.RelationshipMutation{touch}, nil
}

func (k referenceKind[T]) insert(ctx context.Context, db *gorm.DB, command CreateReferenceCommand) (*T, error) {
	name, err := normalizeReferenceName(command.Name, k.maxLength)
	if err != nil {
		return nil, err
	}

	normalizedSlug := strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(name))
	if command.Slug != nil {
		normalizedSlug = *command.Slug
	}
	normalizedSlug, err = normalizeReferenceSlug(normalizedSlug, k.maxLength)
	if err != nil {
		return nil, err
	}

	record := k.newRecord(name, normalizedSlug, command.Description)
	if err := db.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(record).Error; err != nil {
		return nil, classifyReferenceUniqueViolation(k.resource, name, normalizedSlug, err)
	}
	return record, nil
}

func (k referenceKind[T]) lockForMutation(ctx context.Context, db *gorm.DB, id string) (*T, error) {
	record := new(T)
	if err := db.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(record, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound(k.resource, id)
		}
		return nil, err
	}
	return record, nil
}

// updateLocked applies command to the locked record, rereads it, and returns
// the changed fields.
func (k referenceKind[T]) updateLocked(ctx context.Context, db *gorm.DB, record *T, command UpdateReferenceCommand) ([]string, error) {
	_, currentName, currentSlug := k.values(record)
	nextName, nextSlug := currentName, currentSlug
	updates := structured.Fields{}
	changedFields := make([]string, 0, 3)
	var err error
	if command.Name != nil {
		if nextName, err = normalizeReferenceName(*command.Name, k.maxLength); err != nil {
			return nil, err
		}
		if nextName != currentName {
			updates["name"] = nextName
			changedFields = append(changedFields, "name")
		}
	}
	if command.Slug != nil {
		if nextSlug, err = normalizeReferenceSlug(*command.Slug, k.maxLength); err != nil {
			return nil, err
		}
		if nextSlug != currentSlug {
			updates["slug"] = nextSlug
			changedFields = append(changedFields, "slug")
		}
	}
	if command.Description != nil {
		if k.description == nil {
			return nil, domainerr.InvalidField("description", "is not supported")
		}
		currentDescription := k.description(record)
		if currentDescription == nil || *currentDescription != *command.Description {
			updates["description"] = *command.Description
			changedFields = append(changedFields, "description")
		}
	}

	if len(updates) > 0 {
		if err := db.WithContext(ctx).Model(record).Updates(updates).Error; err != nil {
			return nil, classifyReferenceUniqueViolation(k.resource, nextName, nextSlug, err)
		}
	}
	if err := db.WithContext(ctx).First(record, "id = ?", command.ID).Error; err != nil {
		return nil, err
	}
	return changedFields, nil
}

// deleteLocked relies on the caller's root row lock. PostgreSQL then
// serializes any FK insertion against that lock, so the count and delete are
// one authority boundary rather than a stale UI-derived decision.
func (k referenceKind[T]) deleteLocked(ctx context.Context, db *gorm.DB, id string, record *T) error {
	for _, relation := range k.relations {
		var count int64
		if err := db.WithContext(ctx).Table(relation.table).Where(relation.column+" = ?", id).Limit(1).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return domainerr.Precondition(k.resource + " is still referenced")
		}
	}
	return db.WithContext(ctx).Delete(record).Error
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
