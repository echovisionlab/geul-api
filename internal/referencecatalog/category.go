package referencecatalog

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// categorySortConfig defines allowed sort fields for categories
var categorySortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"slug":       "slug",
		"created_at": "created_at",
		"updated_at": "updated_at",
	},
	DefaultSort: "name ASC",
}

var categoryListCRUD = referenceCRUD[model.Category]{
	resource: "category",
	filters:  categoryFilterConfig,
	sorts:    &categorySortConfig,
	newRecord: func(name, slug string, description *string) *model.Category {
		return &model.Category{Name: name, Slug: slug, Description: description, CreatedAt: time.Now()}
	},
	values: func(category *model.Category) (string, string) {
		return category.Name, category.Slug
	},
	description: func(category *model.Category) *string { return category.Description },
}

// CategoryService implements the CategoryService Connect handler
type CategoryService struct {
	managev1connect.UnimplementedCategoryServiceHandler
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	auditWriter domainaudit.Appender
	menuTargets MenuTargets
}

// NewCategoryService creates a new CategoryService
func NewCategoryService(db *gorm.DB, menuTargets MenuTargets, spiceDB *auth.SpiceDBClient) *CategoryService {
	if db == nil {
		panic("db is required")
	}
	if menuTargets == nil {
		panic("menu targets are required")
	}
	if spiceDB == nil {
		panic("spiceDB is required")
	}
	return &CategoryService{db: db, spiceDB: spiceDB, menuTargets: menuTargets}
}

func NewAuditedCategoryService(db *gorm.DB, auditWriter domainaudit.Appender, menuTargets MenuTargets, spiceDB *auth.SpiceDBClient) *CategoryService {
	if auditWriter == nil {
		panic("category audit writer is required")
	}
	service := NewCategoryService(db, menuTargets, spiceDB)
	service.auditWriter = auditWriter
	return service
}

// GetCategory retrieves a category by ID
// GetCategoryBySlug retrieves a category by slug
// ListCategories returns a paginated list of categories (public)
func (s *CategoryService) ListCategories(
	ctx context.Context,
	req *connect.Request[managev1.ListCategoriesRequest],
) (*connect.Response[managev1.ListCategoriesResponse], error) {
	categories, total, page, err := categoryListCRUD.list(ctx, s.db, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	protoCategories := make([]*managev1.Category, len(categories))
	for i := range categories {
		protoCategories[i] = toProtoCategory(&categories[i])
	}

	return connect.NewResponse(&managev1.ListCategoriesResponse{
		Categories: protoCategories,
		Pagination: page.BuildResponse(total),
	}), nil
}

// ListCategoriesAdmin returns a paginated list of categories with stats (admin only)
func (s *CategoryService) ListCategoriesAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListCategoriesAdminRequest],
) (*connect.Response[managev1.ListCategoriesAdminResponse], error) {
	can, err := policyv1.Category.List()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}

	categories, total, page, err := categoryListCRUD.list(ctx, s.db, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	categoryIDs := make([]string, len(categories))
	for i, c := range categories {
		categoryIDs[i] = c.ID
	}

	postCounts, err := loadReferenceCounts(ctx, s.db, "post_category", "category_id", categoryIDs)
	if err != nil {
		return nil, err
	}
	releaseCounts, err := loadReferenceCounts(ctx, s.db, "release_category", "category_id", categoryIDs)
	if err != nil {
		return nil, err
	}

	// Build response with stats
	categoriesWithStats := make([]*managev1.CategoryWithStats, len(categories))
	for i := range categories {
		categoriesWithStats[i] = &managev1.CategoryWithStats{
			Category:  toProtoCategory(&categories[i]),
			PostCount: postCounts[categories[i].ID] + releaseCounts[categories[i].ID],
		}
	}

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
	can, err := policyv1.Category.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}

	var category *model.Category
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		var err error
		category, err = categoryListCRUD.create(ctx, tx, req.Msg.Name, req.Msg.Slug, req.Msg.Description)
		if err != nil {
			return err
		}
		if err := domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditCategoryCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewCategoryCreatedAuditRecord(metadata, category.ID)
		}); err != nil {
			return err
		}
		policyTouch, err := policyv1.Category.TouchPolicy(category.ID)
		if err != nil {
			return err
		}
		policyDelete, err := policyv1.Category.DeletePolicy(category.ID)
		if err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{policyTouch}, []policyv1.RelationshipMutation{policyDelete})
	})
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
	can, err := policyv1.Category.Edit(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}

	var category *model.Category
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var changedFields []string
		var err error
		category, err = categoryListCRUD.lockForMutation(ctx, tx, req.Msg.Id)
		if err != nil {
			return err
		}
		previousSlug := category.Slug
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		category, changedFields, err = categoryListCRUD.updateLocked(ctx, tx, req.Msg.Id, category, req.Msg.Name, req.Msg.Slug, req.Msg.Description)
		if err != nil || len(changedFields) == 0 {
			return err
		}
		if req.Msg.Slug != nil && previousSlug != category.Slug {
			if err := s.menuTargets.UpdateSlug(ctx, tx, MenuTargetSlugChange{
				Target:   MenuTarget{LinkType: "category", ID: category.ID, Slug: previousSlug},
				NextSlug: category.Slug,
			}); err != nil {
				return err
			}
		}
		return domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditCategoryUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewCategoryMetadataUpdatedAuditRecord(metadata, category.ID, changedFields)
		})
	}); err != nil {
		return nil, err
	}

	return connect.NewResponse(toProtoCategory(category)), nil
}

// DeleteCategory deletes a category (admin only)
func (s *CategoryService) DeleteCategory(
	ctx context.Context,
	req *connect.Request[managev1.DeleteCategoryRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	can, err := policyv1.Category.Delete(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}

	var category *model.Category
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		var err error
		category, err = categoryListCRUD.lockForMutation(ctx, tx, req.Msg.Id)
		if err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if err := categoryListCRUD.deleteLockedWithRelationGuards(ctx, tx, req.Msg.Id, category,
			referenceRelationGuard{table: "post_category", column: "category_id"},
			referenceRelationGuard{table: "release_category", column: "category_id"},
		); err != nil {
			return err
		}
		if err := s.menuTargets.Remove(ctx, tx, MenuTarget{LinkType: "category", ID: category.ID, Slug: category.Slug}); err != nil {
			return err
		}
		if err := domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditCategoryDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewCategoryDeletedAuditRecord(metadata, category.ID)
		}); err != nil {
			return err
		}
		policyDelete, err := policyv1.Category.DeletePolicy(category.ID)
		if err != nil {
			return err
		}
		policyTouch, err := policyv1.Category.TouchPolicy(category.ID)
		if err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{policyDelete}, []policyv1.RelationshipMutation{policyTouch})
	})
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

// Note: ReorderCategories removed - categories are ordered by name, no sort_order column

// ==================== Helper Methods ====================

// toProtoCategory converts a model.Category to protobuf Category
func toProtoCategory(c *model.Category) *managev1.Category {
	category := &managev1.Category{
		Id:        c.ID,
		Name:      c.Name,
		Slug:      &c.Slug,
		CreatedAt: timestamppb.New(c.CreatedAt),
	}

	if c.Description != nil {
		category.Description = c.Description
	}
	if c.UpdatedAt != nil {
		category.UpdatedAt = timestamppb.New(*c.UpdatedAt)
	}

	return category
}
