package form

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// GetFormQuery reads a Form by its ID or slug.
type GetFormQuery struct {
	IDOrSlug string
}

// CheckFormSlugAvailableQuery asks whether Slug is free for a new Form or,
// with ExcludeID, for that Form.
type CheckFormSlugAvailableQuery struct {
	Slug      string
	ExcludeID string
}

// Page is one page of a listing; a zero Limit takes the default.
type Page struct {
	Limit  int32
	Offset int32
}

// ListFormsAdminQuery lists every Form, drafts included.
type ListFormsAdminQuery struct {
	Page    Page
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// GetForm returns a Form the caller may view; another Form is not found.
func (s *Service) GetForm(ctx context.Context, query GetFormQuery) (*managev1.Form, error) {
	var form model.Form
	lookup := s.db.WithContext(ctx)
	if IsValidUUID(query.IDOrSlug) {
		lookup = lookup.Where("id = ?", query.IDOrSlug)
	} else {
		lookup = lookup.Where("slug = ?", query.IDOrSlug)
	}
	notFound := domainerr.ResourceNotFound("form", query.IDOrSlug)
	if err := lookup.First(&form).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, notFound
		}
		return nil, err
	}
	if err := s.requireVisibleFormAction(ctx, form.ID, formActionView, notFound); err != nil {
		return nil, err
	}
	return s.toProtoForm(ctx, &form)
}

// CheckFormSlugAvailable reports whether the slug is free among Forms and
// Page routes. Checking for an existing Form requires editing it.
func (s *Service) CheckFormSlugAvailable(ctx context.Context, query CheckFormSlugAvailableQuery) (*managev1.CheckFormSlugAvailableResponse, error) {
	if err := pathslug.Validate(query.Slug); err != nil {
		return nil, err
	}
	if err := s.authorizeFormSlugCheck(ctx, query.ExcludeID); err != nil {
		return nil, err
	}
	available, err := isSlugAvailable(ctx, s.db, query.Slug, query.ExcludeID)
	if err != nil {
		return nil, err
	}
	if available {
		if available, err = s.routes.SlugAvailable(ctx, s.db, query.Slug, query.ExcludeID); err != nil {
			return nil, err
		}
	}
	return &managev1.CheckFormSlugAvailableResponse{Available: available}, nil
}

func (s *Service) authorizeFormSlugCheck(ctx context.Context, excludeID string) error {
	if excludeID == "" {
		return s.requirePlatformFormAction(ctx, policyv1.Form.Create)
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.Form{}).Where("id = ?", excludeID).Count(&count).Error; err != nil {
		return err
	}
	notFound := domainerr.ResourceNotFound("form", excludeID)
	if count == 0 {
		return notFound
	}
	return s.requireVisibleFormAction(ctx, excludeID, formActionEdit, notFound)
}

// ListFormsAdmin returns a page of Forms with their submission counts.
func (s *Service) ListFormsAdmin(ctx context.Context, query ListFormsAdminQuery) (*managev1.ListFormsAdminResponse, error) {
	if err := s.requirePlatformFormAction(ctx, policyv1.Form.List); err != nil {
		return nil, err
	}
	listing, err := FormFilterConfig.ApplyFilters(s.db.WithContext(ctx).Model(&model.Form{}), query.Filters)
	if err != nil {
		return nil, err
	}
	var total int64
	if err := listing.Count(&total).Error; err != nil {
		return nil, err
	}
	limit := int32(20)
	if query.Page.Limit > 0 {
		limit = query.Page.Limit
	}
	offset := query.Page.Offset
	if listing, err = formSortConfig.ApplySort(listing, query.Sorts); err != nil {
		return nil, err
	}
	var forms []model.Form
	if err := listing.Limit(int(limit)).Offset(int(offset)).Find(&forms).Error; err != nil {
		return nil, err
	}
	summaries, err := s.formSummaries(ctx, forms)
	if err != nil {
		return nil, err
	}
	return &managev1.ListFormsAdminResponse{
		Forms: summaries,
		Pagination: &commonv1.PaginationResponse{
			Total: int32(total), Limit: limit, Offset: offset, HasMore: offset+limit < int32(total),
		},
	}, nil
}

// formSummaries projects forms with their source titles and submission
// counts.
func (s *Service) formSummaries(ctx context.Context, forms []model.Form) ([]*managev1.FormSummary, error) {
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
	summaries := make([]*managev1.FormSummary, len(forms))
	for i := range forms {
		summaries[i] = toProtoFormSummaryWithSubmissionCount(&forms[i], sourceTitles[forms[i].ID], submissionCounts[forms[i].ID])
	}
	return summaries, nil
}

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
