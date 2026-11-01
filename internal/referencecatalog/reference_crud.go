package referencecatalog

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
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

// referenceKind describes one reference catalog kind. The kind files hold the
// descriptor and the use cases; the list, create, update and delete flows
// below are shared, and the Store knows each kind's table, the relations
// that use it and its list filters and sorts.
type referenceKind struct {
	kind      ReferenceKind
	maxLength int
	policy    referencePolicy
	audit     referenceAudit
	// menuLinkType is set for kinds that menu items can link to; slug changes
	// and deletions then update those menu targets in the same transaction.
	menuLinkType string
	// described is set for kinds with a description column.
	described bool
}

func (k referenceKind) resource() string {
	return string(k.kind)
}

func (k referenceKind) list(ctx context.Context, s *Service, query ReferenceListQuery) (Page[Reference], error) {
	page := ListPage{Filters: query.Filters, Sorts: query.Sorts, Limit: 100, Offset: query.Offset}
	if query.Limit > 0 {
		page.Limit = query.Limit
	}
	var listed Page[Reference]
	err := s.units.Read(ctx, func(store Store) error {
		references, total, err := store.ListReferences(ctx, k.kind, page)
		if err != nil {
			return err
		}
		listed = Page[Reference]{Items: references, Pagination: paginationOf(page.Limit, page.Offset, total)}
		return nil
	})
	return listed, err
}

// listAdmin lists the kind for administrators with each row's usage count.
func (k referenceKind) listAdmin(ctx context.Context, s *Service, query ReferenceListQuery) (Page[ReferenceUsage], error) {
	can, err := k.policy.List()
	if err != nil {
		return Page[ReferenceUsage]{}, err
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return Page[ReferenceUsage]{}, err
	}
	page := ListPage{Filters: query.Filters, Sorts: query.Sorts, Limit: 100, Offset: query.Offset}
	if query.Limit > 0 {
		page.Limit = query.Limit
	}
	var listed Page[ReferenceUsage]
	err = s.units.Read(ctx, func(store Store) error {
		references, total, err := store.ListReferences(ctx, k.kind, page)
		if err != nil {
			return err
		}
		ids := make([]string, len(references))
		for i := range references {
			ids[i] = references[i].ID
		}
		usage, err := store.ReferenceUsage(ctx, k.kind, ids)
		if err != nil {
			return err
		}
		items := make([]ReferenceUsage, len(references))
		for i := range references {
			items[i] = ReferenceUsage{Reference: references[i], Count: usage[ids[i]]}
		}
		listed = Page[ReferenceUsage]{Items: items, Pagination: paginationOf(page.Limit, page.Offset, total)}
		return nil
	})
	return listed, err
}

func (k referenceKind) create(ctx context.Context, s *Service, command CreateReferenceCommand) (Reference, error) {
	can, err := k.policy.Create()
	if err != nil {
		return Reference{}, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return Reference{}, err
	}
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[Reference, Store]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (Reference, error) {
			return k.insert(ctx, store, command)
		},
		Audit: func(record Reference) mutation.Audit {
			return mutation.RequestAudit(k.audit.created, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return k.audit.newCreated(metadata, record.ID)
			})
		},
		Relationships: func(record Reference) (apply, compensate []policyv1.RelationshipMutation, err error) {
			return referencePolicyRelationships(k.policy, record.ID, true)
		},
	})
}

type updatedReference struct {
	record  Reference
	changed []string
}

func (k referenceKind) update(ctx context.Context, s *Service, command UpdateReferenceCommand) (_ Reference, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String(k.resource()+"_id", command.ID))

	can, err := k.policy.Edit(command.ID)
	if err != nil {
		return Reference{}, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return Reference{}, err
	}
	var record Reference
	result, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[updatedReference, Store]{
		Lock: func(ctx context.Context, store Store) (err error) {
			record, err = k.lockForMutation(ctx, store, command.ID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (updatedReference, error) {
			updated, changed, err := k.updateLocked(ctx, store, record, command)
			if err != nil || len(changed) == 0 {
				return updatedReference{record: updated}, err
			}
			if k.menuLinkType != "" && record.Slug != updated.Slug {
				if err := store.UpdateMenuTargetSlug(ctx, MenuTargetSlugChange{
					Target:   MenuTarget{LinkType: k.menuLinkType, ID: updated.ID, Slug: record.Slug},
					NextSlug: updated.Slug,
				}); err != nil {
					return updatedReference{}, err
				}
			}
			return updatedReference{record: updated, changed: changed}, nil
		},
		Audit: func(result updatedReference) mutation.Audit {
			if len(result.changed) == 0 {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(k.audit.updated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return k.audit.newUpdated(metadata, result.record.ID, result.changed)
			})
		},
	})
	if err != nil {
		return Reference{}, err
	}
	return result.record, nil
}

func (k referenceKind) delete(ctx context.Context, s *Service, command DeleteReferenceCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String(k.resource()+"_id", command.ID))

	can, err := k.policy.Delete(command.ID)
	if err != nil {
		return err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return err
	}
	var record Reference
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Lock: func(ctx context.Context, store Store) (err error) {
			record, err = k.lockForMutation(ctx, store, command.ID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (string, error) {
			// The root row lock serializes any foreign-key insertion against
			// this check, so the check and delete are one authority boundary
			// rather than a stale UI-derived decision.
			inUse, err := store.ReferenceInUse(ctx, k.kind, command.ID)
			if err != nil {
				return "", err
			}
			if inUse {
				return "", domainerr.Precondition(k.resource() + " is still referenced")
			}
			if err := store.DeleteReference(ctx, k.kind, record.ID); err != nil {
				return "", err
			}
			if k.menuLinkType != "" {
				if err := store.RemoveMenuTarget(ctx, MenuTarget{LinkType: k.menuLinkType, ID: record.ID, Slug: record.Slug}); err != nil {
					return "", err
				}
			}
			return record.ID, nil
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

func (k referenceKind) insert(ctx context.Context, store Store, command CreateReferenceCommand) (Reference, error) {
	name, err := normalizeReferenceName(command.Name, k.maxLength)
	if err != nil {
		return Reference{}, err
	}

	normalizedSlug := strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(name))
	if command.Slug != nil {
		normalizedSlug = *command.Slug
	}
	normalizedSlug, err = normalizeReferenceSlug(normalizedSlug, k.maxLength)
	if err != nil {
		return Reference{}, err
	}

	value := Reference{Name: name, Slug: normalizedSlug}
	if k.described {
		value.Description = command.Description
	}
	record, err := store.InsertReference(ctx, k.kind, value)
	if err != nil {
		return Reference{}, k.duplicate(name, normalizedSlug, err)
	}
	return record, nil
}

func (k referenceKind) lockForMutation(ctx context.Context, store Store, id string) (Reference, error) {
	record, err := store.LockReference(ctx, k.kind, id)
	if errors.Is(err, ErrReferenceNotFound) {
		return Reference{}, domainerr.ResourceNotFound(k.resource(), id)
	}
	return record, err
}

// updateLocked applies command to the locked record and returns the record
// as stored and the changed fields.
func (k referenceKind) updateLocked(ctx context.Context, store Store, record Reference, command UpdateReferenceCommand) (Reference, []string, error) {
	nextName, nextSlug := record.Name, record.Slug
	var changes ReferenceChanges
	changedFields := make([]string, 0, 3)
	var err error
	if command.Name != nil {
		if nextName, err = normalizeReferenceName(*command.Name, k.maxLength); err != nil {
			return Reference{}, nil, err
		}
		if nextName != record.Name {
			changes.Name = &nextName
			changedFields = append(changedFields, "name")
		}
	}
	if command.Slug != nil {
		if nextSlug, err = normalizeReferenceSlug(*command.Slug, k.maxLength); err != nil {
			return Reference{}, nil, err
		}
		if nextSlug != record.Slug {
			changes.Slug = &nextSlug
			changedFields = append(changedFields, "slug")
		}
	}
	if command.Description != nil {
		if !k.described {
			return Reference{}, nil, domainerr.InvalidField("description", "is not supported")
		}
		if record.Description == nil || *record.Description != *command.Description {
			changes.Description = command.Description
			changedFields = append(changedFields, "description")
		}
	}
	if len(changedFields) == 0 {
		return record, nil, nil
	}
	updated, err := store.UpdateReference(ctx, k.kind, record.ID, changes)
	if err != nil {
		return Reference{}, nil, k.duplicate(nextName, nextSlug, err)
	}
	return updated, changedFields, nil
}

// duplicate answers a write the kind's unique name or slug refused; other
// errors pass through.
func (k referenceKind) duplicate(name, slug string, err error) error {
	switch {
	case errors.Is(err, ErrNameTaken):
		return domainerr.Duplicate(k.resource(), "name", name).WithCause(err)
	case errors.Is(err, ErrSlugTaken):
		return domainerr.Duplicate(k.resource(), "slug", slug).WithCause(err)
	case errors.Is(err, ErrReferenceTaken):
		return domainerr.New(domainerr.AlreadyExists, "", k.resource()+" with the same name or slug already exists").WithCause(err)
	default:
		return err
	}
}
