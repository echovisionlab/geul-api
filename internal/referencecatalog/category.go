package referencecatalog

import (
	"context"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// categoryKind is used by Posts and Releases, and menu items can link to it.
var categoryKind = referenceKind{
	kind:      KindCategory,
	maxLength: referenceValueMaxLength,
	policy:    policyv1.Category,
	audit: referenceAudit{
		created:    sharedtelemetry.AuditCategoryCreated,
		updated:    sharedtelemetry.AuditCategoryUpdated,
		deleted:    sharedtelemetry.AuditCategoryDeleted,
		newCreated: sharedtelemetry.NewCategoryCreatedAuditRecord,
		newUpdated: sharedtelemetry.NewCategoryMetadataUpdatedAuditRecord,
		newDeleted: sharedtelemetry.NewCategoryDeletedAuditRecord,
	},
	menuLinkType: "category",
	described:    true,
}

func (s *Service) ListCategories(ctx context.Context, query ReferenceListQuery) (Page[Reference], error) {
	return categoryKind.list(ctx, s, query)
}

func (s *Service) ListCategoriesAdmin(ctx context.Context, query ReferenceListQuery) (Page[ReferenceUsage], error) {
	return categoryKind.listAdmin(ctx, s, query)
}

func (s *Service) CreateCategory(ctx context.Context, command CreateReferenceCommand) (Reference, error) {
	return categoryKind.create(ctx, s, command)
}

func (s *Service) UpdateCategory(ctx context.Context, command UpdateReferenceCommand) (Reference, error) {
	return categoryKind.update(ctx, s, command)
}

func (s *Service) DeleteCategory(ctx context.Context, command DeleteReferenceCommand) error {
	return categoryKind.delete(ctx, s, command)
}
