package referencecatalog

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// categoryKind is used by Posts and Releases, and menu items can link to it.
var categoryKind = referenceKind[model.Category]{
	resource:  "category",
	maxLength: referenceValueMaxLength,
	filters:   categoryFilterConfig,
	sorts: &queryutil.SortConfig{
		AllowedFields: map[string]string{
			"name":       "name",
			"slug":       "slug",
			"created_at": "created_at",
			"updated_at": "updated_at",
		},
		DefaultSort: "name ASC",
	},
	policy: policyv1.Category,
	audit: referenceAudit{
		created:    sharedtelemetry.AuditCategoryCreated,
		updated:    sharedtelemetry.AuditCategoryUpdated,
		deleted:    sharedtelemetry.AuditCategoryDeleted,
		newCreated: sharedtelemetry.NewCategoryCreatedAuditRecord,
		newUpdated: sharedtelemetry.NewCategoryMetadataUpdatedAuditRecord,
		newDeleted: sharedtelemetry.NewCategoryDeletedAuditRecord,
	},
	relations: []referenceRelation{
		{table: "post_category", column: "category_id"},
		{table: "release_category", column: "category_id"},
	},
	menuLinkType: "category",
	newRecord: func(name, slug string, description *string) *model.Category {
		return &model.Category{Name: name, Slug: slug, Description: description, CreatedAt: dbtime.Now()}
	},
	values: func(category *model.Category) (string, string, string) {
		return category.ID, category.Name, category.Slug
	},
	description: func(category *model.Category) *string { return category.Description },
}

func (s *Service) ListCategories(ctx context.Context, query ReferenceListQuery) (Page[*managev1.Category], error) {
	return listReferences(ctx, s, categoryKind, query, toProtoCategory)
}

func (s *Service) ListCategoriesAdmin(ctx context.Context, query ReferenceListQuery) (Page[*managev1.CategoryWithStats], error) {
	// PostCount predates Release categories and counts both owners.
	return listReferencesAdmin(ctx, s, categoryKind, query, func(category *model.Category, usage int32) *managev1.CategoryWithStats {
		return &managev1.CategoryWithStats{Category: toProtoCategory(category), PostCount: usage}
	})
}

func (s *Service) CreateCategory(ctx context.Context, command CreateReferenceCommand) (*managev1.Category, error) {
	category, err := categoryKind.create(ctx, s, command)
	return projectReference(category, err, toProtoCategory)
}

func (s *Service) UpdateCategory(ctx context.Context, command UpdateReferenceCommand) (*managev1.Category, error) {
	category, err := categoryKind.update(ctx, s, command)
	return projectReference(category, err, toProtoCategory)
}

func (s *Service) DeleteCategory(ctx context.Context, command DeleteReferenceCommand) error {
	return categoryKind.delete(ctx, s, command)
}

func toProtoCategory(c *model.Category) *managev1.Category {
	category := &managev1.Category{
		Id:          c.ID,
		Name:        c.Name,
		Slug:        &c.Slug,
		Description: c.Description,
		CreatedAt:   timestamppb.New(c.CreatedAt),
	}
	if c.UpdatedAt != nil {
		category.UpdatedAt = timestamppb.New(*c.UpdatedAt)
	}
	return category
}
