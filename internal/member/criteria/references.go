package criteria

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// criteriaViews returns Member criteria with what targets each of them.
func criteriaViews(ctx context.Context, store Store, criteriaSet []Criteria) ([]CriteriaView, error) {
	views := make([]CriteriaView, len(criteriaSet))
	if len(criteriaSet) == 0 {
		return views, nil
	}
	ids := make([]string, len(criteriaSet))
	for i, criteria := range criteriaSet {
		ids[i] = criteria.ID
	}
	references, err := store.CriteriaReferences(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i, criteria := range criteriaSet {
		views[i] = CriteriaView{Criteria: criteria, References: references[criteria.ID]}
	}
	return views, nil
}

// criteriaView reads a criteria and what targets it, as a mutation returns it.
func criteriaView(ctx context.Context, store Store, id string) (CriteriaView, error) {
	criteria, err := store.Criteria(ctx, id)
	if err != nil {
		return CriteriaView{}, criteriaNotFound(err)
	}
	views, err := criteriaViews(ctx, store, []Criteria{criteria})
	if err != nil {
		return CriteriaView{}, err
	}
	return views[0], nil
}

func ensureCriteriaMutableForActiveDelivery(ctx context.Context, store Store, criteriaID string) error {
	campaign, deliveryRun, err := store.CriteriaInActiveDelivery(ctx, criteriaID)
	if err != nil {
		return err
	}
	if campaign {
		return domainerr.Precondition("Member criteria is used by a scheduled or sending campaign")
	}
	if deliveryRun {
		return domainerr.Precondition("Member criteria is frozen by an active delivery run")
	}
	return nil
}

// criteriaNotFound answers a missing criteria; other errors pass unchanged.
func criteriaNotFound(err error) error {
	if errors.Is(err, ErrCriteriaNotFound) {
		return errCriteriaNotFound()
	}
	return err
}

func errCriteriaNotFound() error {
	return domainerr.New(domainerr.NotFound, "", "criteria not found")
}
