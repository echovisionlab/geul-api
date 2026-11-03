package criteria

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Page selects one page of a criteria list: the shared query language's
// filters and sorts, and the normalized limit and offset.
type Page struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Limit   int32
	Offset  int32
}

// GetCriteriaQuery reads one criteria.
type GetCriteriaQuery struct {
	ID string
}

// GetCriteria returns the criteria with its configuration, reference counts
// and, when the counter answers, its estimated recipients.
func (s *Service) GetCriteria(ctx context.Context, query GetCriteriaQuery) (CriteriaView, error) {
	can, err := policyv1.MemberCriteria.View(query.ID)
	if err != nil {
		return CriteriaView{}, err
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return CriteriaView{}, err
	}
	var view CriteriaView
	if err := s.units.Read(ctx, func(store Store) (err error) {
		view, err = criteriaView(ctx, store, query.ID)
		return err
	}); err != nil {
		return CriteriaView{}, err
	}
	if count, err := s.estimateCount(ctx, view.Criteria); err == nil {
		view.EstimatedCount = &count
	} else {
		slog.ErrorContext(ctx, "Member criteria count failed; returning the criteria without a count", "domain", "member_criteria", "event", "member_criteria.operation_failed", "operation", "count_matching_members", "outcome", "failed", "member_criteria_id", view.Criteria.ID, failurecause.Attr(err))
	}
	return view, nil
}

// ListCriteriaSetQuery pages through Member criteria for administrators.
type ListCriteriaSetQuery struct {
	IncludeArchived bool
	// Limit defaults to 20 and is capped at 100.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// CriteriaPage is one page of Member criteria and the total the filters select.
type CriteriaPage struct {
	CriteriaSet []CriteriaView
	Total       int64
	Limit       int32
	Offset      int32
}

// ListCriteriaSet returns one page of Member criteria with their configuration and
// reference counts.
func (s *Service) ListCriteriaSet(ctx context.Context, query ListCriteriaSetQuery) (CriteriaPage, error) {
	can, err := policyv1.MemberCriteria.List()
	if err != nil {
		return CriteriaPage{}, err
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return CriteriaPage{}, err
	}
	page := listPage(query.Limit, query.Offset, query.Filters, query.Sorts)
	result := CriteriaPage{Limit: page.Limit, Offset: page.Offset}
	if err := s.units.Read(ctx, func(store Store) error {
		criteriaSet, total, err := store.ListCriteriaSet(ctx, query.IncludeArchived, page)
		if err != nil {
			return err
		}
		result.Total = total
		result.CriteriaSet, err = criteriaViews(ctx, store, criteriaSet)
		return err
	}); err != nil {
		return CriteriaPage{}, err
	}
	return result, nil
}

// ListAuthenticatedAccessCriteriaSetQuery pages through the active Member criteria a
// download policy may grant access to.
type ListAuthenticatedAccessCriteriaSetQuery struct {
	// Limit defaults to 20 and is capped at 100.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// AccessCriteriaPage is one page of Member criteria without their filters, and the
// total the filters select.
type AccessCriteriaPage struct {
	CriteriaSet []Criteria
	Total       int64
	Limit       int32
	Offset      int32
}

// ListAuthenticatedAccessCriteriaSet returns the Member criteria that can grant
// authenticated download access, for authors editing a policy.
func (s *Service) ListAuthenticatedAccessCriteriaSet(ctx context.Context, query ListAuthenticatedAccessCriteriaSetQuery) (AccessCriteriaPage, error) {
	if err := requireDownloadPolicyAuthor(ctx, s.spiceDB); err != nil {
		return AccessCriteriaPage{}, err
	}
	page := listPage(query.Limit, query.Offset, query.Filters, query.Sorts)
	result := AccessCriteriaPage{Limit: page.Limit, Offset: page.Offset}
	if err := s.units.Read(ctx, func(store Store) (err error) {
		result.CriteriaSet, result.Total, err = store.ListAuthenticatedAccessCriteriaSet(ctx, page)
		return err
	}); err != nil {
		return AccessCriteriaPage{}, err
	}
	for _, criteria := range result.CriteriaSet {
		if !criteria.GrantsAuthenticatedAccess() {
			return AccessCriteriaPage{}, errors.New("authenticated Member criteria query returned an ineligible criteria")
		}
	}
	return result, nil
}

func (s *Service) estimateCount(ctx context.Context, criteria Criteria) (int32, error) {
	switch criteria.Type {
	case KindAllMembers, KindMemberTags, KindFilter:
	default:
		return 0, fmt.Errorf("unknown criteria type: %s", criteria.Type)
	}
	count, err := s.recipientCounter.Count(ctx, criteria)
	if err != nil {
		return 0, err
	}
	return int32(count), nil
}

// listPage defaults the limit to 20, caps it at 100 and ignores a negative
// offset.
func listPage(requestedLimit, requestedOffset int32, filters []*commonv1.FilterSpec, sorts []*commonv1.SortSpec) Page {
	limit, offset := int32(20), int32(0)
	if requestedLimit > 0 {
		limit = requestedLimit
	}
	if requestedOffset > 0 {
		offset = requestedOffset
	}
	if limit > 100 {
		limit = 100
	}
	return Page{Filters: filters, Sorts: sorts, Limit: limit, Offset: offset}
}
