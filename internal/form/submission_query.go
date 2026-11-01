package form

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// ListFormSubmissionsQuery lists a Form's submissions. Search matches the
// submitted data; the creation bounds are inclusive from and exclusive
// before.
type ListFormSubmissionsQuery struct {
	FormID          string
	Search          string
	CountryCode     string
	CreatedAtFrom   *time.Time
	CreatedAtBefore *time.Time
	Page            Page
	Sorts           []*commonv1.SortSpec
}

// GetFormSubmissionQuery reads one submission with its Form's schema.
type GetFormSubmissionQuery struct {
	ID string
}

// ListFormSubmissions returns a page of submissions of a Form the caller
// manages and records the personal data access.
func (s *Service) ListFormSubmissions(ctx context.Context, query ListFormSubmissionsQuery) (*managev1.ListFormSubmissionsResponse, error) {
	// An unknown Form is not found for administrators too, as for every
	// submission read.
	if err := s.requireManagedForm(ctx, query.FormID); err != nil {
		return nil, err
	}
	filtered, err := applyFormSubmissionFilters(s.db.WithContext(ctx).Model(&model.FormSubmission{}).Where("form_id = ?", query.FormID), query)
	if err != nil {
		return nil, err
	}
	sorted, err := applyFormSubmissionSort(filtered, query.Sorts)
	if err != nil {
		return nil, err
	}
	var total int64
	if err := filtered.Count(&total).Error; err != nil {
		return nil, err
	}
	pagination := queryutil.ExtractPaginationRaw(query.Page.Limit, query.Page.Offset, queryutil.PaginationConfig{DefaultLimit: 50, MaxLimit: 100})
	var submissions []model.FormSubmission
	if err := sorted.Limit(int(pagination.Limit)).Offset(int(pagination.Offset)).Find(&submissions).Error; err != nil {
		return nil, err
	}
	protoSubmissions := make([]*managev1.FormSubmission, len(submissions))
	for i := range submissions {
		protoSubmissions[i] = toProtoSubmission(&submissions[i])
	}
	if err := s.personalDataAccess.AppendFormSubmissions(ctx, query.FormID); err != nil {
		return nil, securityaccess.Unavailable().WithCause(err)
	}
	return &managev1.ListFormSubmissionsResponse{
		Submissions: protoSubmissions,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   pagination.Limit,
			Offset:  pagination.Offset,
			HasMore: int64(pagination.Offset)+int64(pagination.Limit) < total,
		},
	}, nil
}

// requireManagedForm answers not found for a missing Form and for a Form the
// caller may not manage.
func (s *Service) requireManagedForm(ctx context.Context, formID string) error {
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.Form{}).Where("id = ?", formID).Count(&count).Error; err != nil {
		return err
	}
	notFound := domainerr.ResourceNotFound("form", formID)
	if count == 0 {
		return notFound
	}
	return s.requireVisibleFormAction(ctx, formID, formActionManage, notFound)
}

// GetFormSubmissionWithSchema returns one submission of a Form the caller manages with
// the Form's source schema and records the personal data access.
func (s *Service) GetFormSubmissionWithSchema(ctx context.Context, query GetFormSubmissionQuery) (*managev1.FormSubmissionWithSchema, error) {
	notFound := domainerr.New(domainerr.NotFound, "", "submission not found")
	var submission model.FormSubmission
	if err := s.db.WithContext(ctx).First(&submission, "id = ?", query.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, notFound
		}
		return nil, err
	}
	var form model.Form
	if err := s.db.WithContext(ctx).First(&form, "id = ?", submission.FormID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.New(domainerr.NotFound, "", "form not found")
		}
		return nil, err
	}
	if err := s.requireVisibleFormAction(ctx, form.ID, formActionManage, notFound); err != nil {
		return nil, err
	}
	formSchema, sourceTitle, err := loadFormSourceSchema(ctx, s.db, form.ID)
	if err != nil {
		return nil, err
	}
	if err := s.personalDataAccess.AppendFormSubmission(ctx, submission.ID); err != nil {
		return nil, securityaccess.Unavailable().WithCause(err)
	}
	return &managev1.FormSubmissionWithSchema{
		Submission: toProtoSubmission(&submission),
		FormSchema: formSchema,
		FormTitle:  resolveFormTitle(sourceTitle),
	}, nil
}
