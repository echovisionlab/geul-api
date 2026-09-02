package form

import (
	"connectrpc.com/connect"
	"context"
	"github.com/echovisionlab/geul-api/internal/authz"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

// GetForm retrieves a form by ID or slug.
func (s *FormService) GetForm(
	ctx context.Context,
	req *connect.Request[managev1.GetFormRequest],
) (*connect.Response[managev1.Form], error) {
	var form model.Form
	query := s.db.WithContext(ctx)
	if IsValidUUID(req.Msg.Id) {
		query = query.Where("id = ?", req.Msg.Id)
	} else {
		query = query.Where("slug = ?", req.Msg.Id)
	}
	if err := query.First(&form).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("form", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}

	if err := s.requireFormAction(ctx, form.ID, formActionView); err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return nil, errs.NotFound("form", req.Msg.Id)
		}
		return nil, err
	}

	protoForm, err := s.toProtoForm(ctx, &form)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(protoForm), nil
}

// GetFormBySlug retrieves a form by slug
// CheckFormSlugAvailable checks if a slug is available.
// If exclude_id is set, caller must have access to that form.
func (s *FormService) CheckFormSlugAvailable(
	ctx context.Context,
	req *connect.Request[managev1.CheckFormSlugAvailableRequest],
) (*connect.Response[managev1.CheckFormSlugAvailableResponse], error) {
	if err := validateSlugWithoutSlash(req.Msg.Slug); err != nil {
		return nil, err
	}
	excludeID := ""
	if req.Msg.ExcludeId != nil {
		excludeID = *req.Msg.ExcludeId
	}
	if err := s.authorizeFormSlugCheck(ctx, excludeID); err != nil {
		return nil, err
	}

	available, err := isSlugAvailable(ctx, s.db, req.Msg.Slug, excludeID)
	if err != nil {
		return nil, err
	}
	if available {
		available, err = s.routes.SlugAvailable(ctx, s.db, req.Msg.Slug, excludeID)
		if err != nil {
			return nil, err
		}
	}

	return connect.NewResponse(&managev1.CheckFormSlugAvailableResponse{
		Available: available,
	}), nil
}

func (s *FormService) authorizeFormSlugCheck(ctx context.Context, excludeID string) error {
	if excludeID == "" {
		can, err := policyv1.Form.Create()
		if err != nil {
			return errs.Internal(err)
		}
		return authz.RequirePlatformPermission(ctx, s.spiceDB, can)
	}
	var count int64
	if err := s.db.WithContext(ctx).
		Model(&model.Form{}).
		Where("id = ?", excludeID).
		Count(&count).Error; err != nil {
		return errs.Internal(err)
	}
	if count == 0 {
		return errs.NotFound("form", excludeID)
	}
	if err := s.requireFormAction(ctx, excludeID, formActionEdit); err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return errs.NotFound("form", excludeID)
		}
		return err
	}
	return nil
}

// ListFormsAdmin returns a paginated list of forms
func (s *FormService) ListFormsAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListFormsAdminRequest],
) (*connect.Response[managev1.ListFormsAdminResponse], error) {
	// Admin only - this endpoint returns all forms including drafts
	can, err := policyv1.Form.List()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequirePlatformPermission(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}

	var forms []model.Form
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Form{})

	// Apply filters using FilterConfig
	query, err = FormFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply pagination
	limit := int32(20)
	offset := int32(0)
	if req.Msg.Pagination != nil {
		if req.Msg.Pagination.Limit > 0 {
			limit = req.Msg.Pagination.Limit
		}
		offset = req.Msg.Pagination.Offset
	}

	// Apply sorting
	query, err = formSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&forms).Error; err != nil {
		return nil, errs.Internal(err)
	}

	formIDs := make([]string, 0, len(forms))
	for _, form := range forms {
		formIDs = append(formIDs, form.ID)
	}
	sourceTitles, err := loadFormSourceTitles(ctx, s.db, formIDs)
	if err != nil {
		return nil, err
	}
	submissionCounts, err := loadFormSubmissionCounts(ctx, s.db, formIDs)
	if err != nil {
		return nil, err
	}

	// Convert to proto with submission counts
	protoForms := make([]*managev1.FormSummary, len(forms))
	for i := range forms {
		protoForms[i] = toProtoFormSummaryWithSubmissionCount(
			&forms[i],
			sourceTitles[forms[i].ID],
			submissionCounts[forms[i].ID],
		)
	}

	return connect.NewResponse(&managev1.ListFormsAdminResponse{
		Forms: protoForms,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// CreateForm creates a new form

// formSortConfig defines allowed sort fields for forms
var formSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"created_at": "created_at",
		"updated_at": "updated_at",
		"title":      FormSourceTitleSQL("form"),
		"status":     "status",
		"slug":       "slug",
	},
	DefaultSort: "created_at DESC",
}
