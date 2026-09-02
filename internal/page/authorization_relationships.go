package page

import (
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func pageAuthorizationDeletionBatches(
	pageID string,
	snapshots []auth.RelationshipSnapshot,
) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	deleteMutation, err := policyv1.Page.DeletePolicy(pageID)
	if err != nil {
		return nil, nil, errs.InvalidArgument("page_id", "must be a canonical UUID")
	}
	restoreMutation, err := policyv1.Page.TouchPolicy(pageID)
	if err != nil {
		return nil, nil, errs.Internal(err)
	}
	deletes := make([]policyv1.RelationshipMutation, 0, len(snapshots))
	restores := make([]policyv1.RelationshipMutation, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if !snapshot.Matches(deleteMutation) {
			return nil, nil, errs.FailedPrecondition("page authorization relationships contain an unsupported tuple")
		}
		deletes = append(deletes, deleteMutation)
		restores = append(restores, restoreMutation)
	}
	return deletes, restores, nil
}
