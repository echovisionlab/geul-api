package series

import (
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func seriesAuthorizationSnapshotMutations(
	seriesID string,
	snapshots []auth.RelationshipSnapshot,
) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	deleted := make([]policyv1.RelationshipMutation, 0, len(snapshots))
	restored := make([]policyv1.RelationshipMutation, 0, len(snapshots))
	for index, snapshot := range snapshots {
		pairs := make([]auth.RelationshipMutationPair, 0, 2)
		appendPair := func(deleteMutation policyv1.RelationshipMutation, deleteErr error, restoreMutation policyv1.RelationshipMutation, restoreErr error) error {
			if deleteErr != nil {
				return deleteErr
			}
			if restoreErr != nil {
				return restoreErr
			}
			pair, err := auth.NewRelationshipMutationPair(deleteMutation, restoreMutation)
			if err != nil {
				return err
			}
			pairs = append(pairs, pair)
			return nil
		}
		deletePolicy, deletePolicyErr := policyv1.PostSeries.DeletePolicy(seriesID)
		touchPolicy, touchPolicyErr := policyv1.PostSeries.TouchPolicy(seriesID)
		if err := appendPair(deletePolicy, deletePolicyErr, touchPolicy, touchPolicyErr); err != nil {
			return nil, nil, fmt.Errorf("series policy snapshot %d: %w", index, err)
		}
		actor, actorErr := policyv1.NewAccountIdentityActor(snapshot.SubjectID())
		if actorErr == nil {
			deleteManager, deleteManagerErr := policyv1.PostSeries.DeleteManager(seriesID, actor)
			touchManager, touchManagerErr := policyv1.PostSeries.TouchManager(seriesID, actor)
			if err := appendPair(deleteManager, deleteManagerErr, touchManager, touchManagerErr); err != nil {
				return nil, nil, fmt.Errorf("series manager snapshot %d: %w", index, err)
			}
		}
		var matched auth.RelationshipMutationPair
		for _, pair := range pairs {
			if pair.Matches(snapshot) {
				matched = pair
				break
			}
		}
		if !matched.Delete().Valid() {
			return nil, nil, fmt.Errorf("series relationship snapshot %d is outside the Series contract", index)
		}
		deleted = append(deleted, matched.Delete())
		restored = append(restored, matched.Restore())
	}
	return deleted, restored, nil
}
