package form

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
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

// SubmissionPage is one page of a Form's submissions.
type SubmissionPage struct {
	Submissions []Submission
	Pagination  Pagination
}

// SubmissionWithSchema is one submission with its Form's source schema and
// title.
type SubmissionWithSchema struct {
	Submission Submission
	FormSchema []byte
	FormTitle  string
}

// ListFormSubmissions returns a page of submissions of a Form the caller
// manages and records the personal data access.
func (s *Service) ListFormSubmissions(ctx context.Context, query ListFormSubmissionsQuery) (SubmissionPage, error) {
	// An unknown Form is not found for administrators too, as for every
	// submission read.
	if err := s.requireExistingFormAction(ctx, query.FormID, formActionManage, domainerr.ResourceNotFound("form", query.FormID)); err != nil {
		return SubmissionPage{}, err
	}
	list, err := formSubmissionList(query)
	if err != nil {
		return SubmissionPage{}, err
	}
	pagination := queryutil.ExtractPaginationRaw(query.Page.Limit, query.Page.Offset, queryutil.PaginationConfig{DefaultLimit: 50, MaxLimit: 100})
	list.Limit, list.Offset = pagination.Limit, pagination.Offset
	var page SubmissionPage
	var total int64
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		page.Submissions, total, err = store.ListSubmissions(ctx, list)
		return err
	}); err != nil {
		return SubmissionPage{}, err
	}
	if err := s.personalDataAccess.AppendFormSubmissions(ctx, query.FormID); err != nil {
		return SubmissionPage{}, securityaccess.Unavailable().WithCause(err)
	}
	page.Pagination = Pagination{
		Total:   int32(total),
		Limit:   pagination.Limit,
		Offset:  pagination.Offset,
		HasMore: int64(pagination.Offset)+int64(pagination.Limit) < total,
	}
	return page, nil
}

// GetFormSubmissionWithSchema returns one submission of a Form the caller manages with
// the Form's source schema and records the personal data access.
func (s *Service) GetFormSubmissionWithSchema(ctx context.Context, query GetFormSubmissionQuery) (SubmissionWithSchema, error) {
	notFound := domainerr.New(domainerr.NotFound, "", "submission not found")
	var submission Submission
	var form Form
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		if submission, err = store.FindSubmission(ctx, query.ID); err != nil {
			if errors.Is(err, ErrSubmissionNotFound) {
				return notFound
			}
			return err
		}
		form, err = store.FindForm(ctx, ByID, submission.FormID)
		return formNotFound(err, domainerr.New(domainerr.NotFound, "", "form not found"))
	}); err != nil {
		return SubmissionWithSchema{}, err
	}
	if err := s.requireVisibleFormAction(ctx, form.ID, formActionManage, notFound); err != nil {
		return SubmissionWithSchema{}, err
	}
	var result SubmissionWithSchema
	if err := s.units.Read(ctx, func(store Store) error {
		schema, sourceTitle, err := loadFormSourceSchema(ctx, store, form.ID)
		result = SubmissionWithSchema{Submission: submission, FormSchema: schema, FormTitle: resolveFormTitle(sourceTitle)}
		return err
	}); err != nil {
		return SubmissionWithSchema{}, err
	}
	if err := s.personalDataAccess.AppendFormSubmission(ctx, submission.ID); err != nil {
		return SubmissionWithSchema{}, securityaccess.Unavailable().WithCause(err)
	}
	return result, nil
}
