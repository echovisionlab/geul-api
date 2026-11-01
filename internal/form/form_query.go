package form

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
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

// Pagination is where a page sits among every matching row.
type Pagination struct {
	Total   int32
	Limit   int32
	Offset  int32
	HasMore bool
}

// Summary is a listed Form with its source title and submission count.
type Summary struct {
	Form            Form
	Title           string
	SubmissionCount int64
}

// FormList is one page of Forms.
type FormList struct {
	Forms      []Summary
	Pagination Pagination
}

// GetForm returns a Form the caller may view; another Form is not found.
func (s *Service) GetForm(ctx context.Context, query GetFormQuery) (View, error) {
	notFound := domainerr.ResourceNotFound("form", query.IDOrSlug)
	var form Form
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		form, err = store.FindForm(ctx, LocatorOf(query.IDOrSlug), query.IDOrSlug)
		return err
	}); err != nil {
		return View{}, formNotFound(err, notFound)
	}
	if err := s.requireVisibleFormAction(ctx, form.ID, formActionView, notFound); err != nil {
		return View{}, err
	}
	return s.view(ctx, form)
}

// CheckFormSlugAvailable reports whether the slug is free among Forms and
// Page routes. Checking for an existing Form requires editing it.
func (s *Service) CheckFormSlugAvailable(ctx context.Context, query CheckFormSlugAvailableQuery) (bool, error) {
	if err := pathslug.Validate(query.Slug); err != nil {
		return false, err
	}
	if err := s.authorizeFormSlugCheck(ctx, query.ExcludeID); err != nil {
		return false, err
	}
	var available bool
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		if available, err = isSlugAvailable(ctx, store, query.Slug, query.ExcludeID); err != nil || !available {
			return err
		}
		available, err = store.RouteAvailable(ctx, query.Slug)
		return err
	})
	return available, err
}

func (s *Service) authorizeFormSlugCheck(ctx context.Context, excludeID string) error {
	if excludeID == "" {
		return s.requirePlatformFormAction(ctx, policyv1.Form.Create)
	}
	return s.requireExistingFormAction(ctx, excludeID, formActionEdit, domainerr.ResourceNotFound("form", excludeID))
}

// requireExistingFormAction answers notFound for a missing Form and for a
// Form the caller may not act on.
func (s *Service) requireExistingFormAction(ctx context.Context, formID string, action formObjectAction, notFound error) error {
	var exists bool
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		exists, err = store.FormExists(ctx, formID)
		return err
	}); err != nil {
		return err
	}
	if !exists {
		return notFound
	}
	return s.requireVisibleFormAction(ctx, formID, action, notFound)
}

// ListFormsAdmin returns a page of Forms with their submission counts.
func (s *Service) ListFormsAdmin(ctx context.Context, query ListFormsAdminQuery) (FormList, error) {
	if err := s.requirePlatformFormAction(ctx, policyv1.Form.List); err != nil {
		return FormList{}, err
	}
	limit := int32(20)
	if query.Page.Limit > 0 {
		limit = query.Page.Limit
	}
	offset := query.Page.Offset
	var list FormList
	err := s.units.Read(ctx, func(store Store) error {
		forms, total, err := store.ListForms(ctx, ListForms{Filters: query.Filters, Sorts: query.Sorts, Limit: limit, Offset: offset})
		if err != nil {
			return err
		}
		if list.Forms, err = formSummaries(ctx, store, forms); err != nil {
			return err
		}
		list.Pagination = Pagination{Total: int32(total), Limit: limit, Offset: offset, HasMore: int64(offset)+int64(limit) < total}
		return nil
	})
	if err != nil {
		return FormList{}, err
	}
	return list, nil
}

// formSummaries projects forms with their source titles and submission
// counts.
func formSummaries(ctx context.Context, store Store, forms []Form) ([]Summary, error) {
	formIDs := make([]string, 0, len(forms))
	for _, form := range forms {
		formIDs = append(formIDs, form.ID)
	}
	sourceTitles, err := store.SourceTitles(ctx, formIDs)
	if err != nil {
		return nil, err
	}
	submissionCounts, err := store.SubmissionCounts(ctx, formIDs)
	if err != nil {
		return nil, err
	}
	summaries := make([]Summary, len(forms))
	for i, form := range forms {
		summaries[i] = Summary{Form: form, Title: resolveFormTitle(new(sourceTitles[form.ID])), SubmissionCount: submissionCounts[form.ID]}
	}
	return summaries, nil
}
