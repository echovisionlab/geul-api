package referencecatalog

import (
	"context"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
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

// CategoryService implements the CategoryService Connect handler
type CategoryService struct {
	managev1connect.UnimplementedCategoryServiceHandler
	referenceService
}

// NewCategoryService creates a new CategoryService
func NewCategoryService(db *gorm.DB, menuTargets MenuTargets, spiceDB *auth.SpiceDBClient) *CategoryService {
	return &CategoryService{referenceService: newMenuTargetReferenceService(db, menuTargets, spiceDB)}
}

func NewAuditedCategoryService(db *gorm.DB, auditWriter domainaudit.Appender, menuTargets MenuTargets, spiceDB *auth.SpiceDBClient) *CategoryService {
	requireReferenceAuditWriter("category", auditWriter)
	service := NewCategoryService(db, menuTargets, spiceDB)
	service.auditWriter = auditWriter
	return service
}

// ListCategories returns a paginated list of categories (public)
func (s *CategoryService) ListCategories(
	ctx context.Context,
	req *connect.Request[managev1.ListCategoriesRequest],
) (*connect.Response[managev1.ListCategoriesResponse], error) {
	categories, total, page, err := categoryKind.list(ctx, s.db, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.ListCategoriesResponse{
		Categories: mapReferences(categories, toProtoCategory),
		Pagination: page.BuildResponse(total),
	}), nil
}

// ListCategoriesAdmin returns a paginated list of categories with stats (admin only)
func (s *CategoryService) ListCategoriesAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListCategoriesAdminRequest],
) (*connect.Response[managev1.ListCategoriesAdminResponse], error) {
	categories, usage, total, page, err := categoryKind.listAdmin(ctx, s.referenceService, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	// PostCount predates Release categories and counts both owners.
	categoriesWithStats := mapReferences(categories, func(category *model.Category) *managev1.CategoryWithStats {
		return &managev1.CategoryWithStats{Category: toProtoCategory(category), PostCount: usage[category.ID]}
	})
	return connect.NewResponse(&managev1.ListCategoriesAdminResponse{
		Categories: categoriesWithStats,
		Pagination: page.BuildResponse(total),
	}), nil
}

// CreateCategory creates a new category (admin only)
func (s *CategoryService) CreateCategory(
	ctx context.Context,
	req *connect.Request[managev1.CreateCategoryRequest],
) (*connect.Response[managev1.Category], error) {
	category, err := categoryKind.createAudited(ctx, s.referenceService, req.Msg.Name, req.Msg.Slug, req.Msg.Description)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoCategory(category)), nil
}

// UpdateCategory updates an existing category (admin only)
func (s *CategoryService) UpdateCategory(
	ctx context.Context,
	req *connect.Request[managev1.UpdateCategoryRequest],
) (*connect.Response[managev1.Category], error) {
	category, err := categoryKind.updateAudited(ctx, s.referenceService, req.Msg.Id, req.Msg.Name, req.Msg.Slug, req.Msg.Description)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoCategory(category)), nil
}

// DeleteCategory deletes a category (admin only)
func (s *CategoryService) DeleteCategory(
	ctx context.Context,
	req *connect.Request[managev1.DeleteCategoryRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if err := categoryKind.deleteAudited(ctx, s.referenceService, req.Msg.Id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
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
