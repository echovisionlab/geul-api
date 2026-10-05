package label

import (
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func createLabelRelationshipMutations(resourceID, ownerIdentityID string, parentID *string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	owner, err := policyv1.NewAccountIdentityActor(ownerIdentityID)
	if err != nil {
		return nil, nil, err
	}
	policyTouch, err := policyv1.Label.TouchPolicy(resourceID)
	if err != nil {
		return nil, nil, err
	}
	policyDelete, err := policyv1.Label.DeletePolicy(resourceID)
	if err != nil {
		return nil, nil, err
	}
	ownerTouch, err := policyv1.Label.TouchOwner(resourceID, owner)
	if err != nil {
		return nil, nil, err
	}
	ownerDelete, err := policyv1.Label.DeleteOwner(resourceID, owner)
	if err != nil {
		return nil, nil, err
	}
	apply := []policyv1.RelationshipMutation{policyTouch, ownerTouch}
	compensate := []policyv1.RelationshipMutation{ownerDelete, policyDelete}
	if parentID == nil {
		return apply, compensate, nil
	}
	parentTouch, err := policyv1.Label.TouchParent(resourceID, *parentID)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid Label parent relationship: %w", err)
	}
	parentDelete, err := policyv1.Label.DeleteParent(resourceID, *parentID)
	if err != nil {
		return nil, nil, err
	}
	return append(apply, parentTouch), append([]policyv1.RelationshipMutation{parentDelete}, compensate...), nil
}

func replaceLabelParentRelationshipMutations(resourceID string, previousParentID, nextParentID *string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	apply := make([]policyv1.RelationshipMutation, 0, 2)
	compensate := make([]policyv1.RelationshipMutation, 0, 2)
	if previousParentID != nil {
		remove, removeErr := policyv1.Label.DeleteParent(resourceID, *previousParentID)
		if removeErr != nil {
			return nil, nil, removeErr
		}
		restore, restoreErr := policyv1.Label.TouchParent(resourceID, *previousParentID)
		if restoreErr != nil {
			return nil, nil, restoreErr
		}
		apply = append(apply, remove)
		compensate = append(compensate, restore)
	}
	if nextParentID != nil {
		touch, touchErr := policyv1.Label.TouchParent(resourceID, *nextParentID)
		if touchErr != nil {
			return nil, nil, touchErr
		}
		remove, removeErr := policyv1.Label.DeleteParent(resourceID, *nextParentID)
		if removeErr != nil {
			return nil, nil, removeErr
		}
		apply = append(apply, touch)
		compensate = append([]policyv1.RelationshipMutation{remove}, compensate...)
	}
	if len(apply) == 0 {
		return nil, nil, fmt.Errorf("label parent relationship change is required")
	}
	return apply, compensate, nil
}

func labelDeletionRelationshipMutations(resourceID string, snapshots []auth.RelationshipSnapshot) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	deleted := make([]policyv1.RelationshipMutation, 0, len(snapshots))
	restored := make([]policyv1.RelationshipMutation, 0, len(snapshots))
	for _, snapshot := range snapshots {
		pairs := make([]auth.RelationshipMutationPair, 0, 4)
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
		deletePolicy, deletePolicyErr := policyv1.Label.DeletePolicy(resourceID)
		touchPolicy, touchPolicyErr := policyv1.Label.TouchPolicy(resourceID)
		if err := appendPair(deletePolicy, deletePolicyErr, touchPolicy, touchPolicyErr); err != nil {
			return nil, nil, fmt.Errorf("invalid label policy snapshot descriptor: %w", err)
		}
		actor, actorErr := policyv1.NewAccountIdentityActor(snapshot.SubjectID())
		if actorErr == nil {
			deleteOwner, deleteOwnerErr := policyv1.Label.DeleteOwner(resourceID, actor)
			touchOwner, touchOwnerErr := policyv1.Label.TouchOwner(resourceID, actor)
			if err := appendPair(deleteOwner, deleteOwnerErr, touchOwner, touchOwnerErr); err != nil {
				return nil, nil, fmt.Errorf("invalid label owner snapshot descriptor: %w", err)
			}
			deleteManager, deleteManagerErr := policyv1.Label.DeleteManager(resourceID, actor)
			touchManager, touchManagerErr := policyv1.Label.TouchManager(resourceID, actor)
			if err := appendPair(deleteManager, deleteManagerErr, touchManager, touchManagerErr); err != nil {
				return nil, nil, fmt.Errorf("invalid label manager snapshot descriptor: %w", err)
			}
		}
		deleteParent, deleteParentErr := policyv1.Label.DeleteParent(snapshot.ResourceID(), snapshot.SubjectID())
		touchParent, touchParentErr := policyv1.Label.TouchParent(snapshot.ResourceID(), snapshot.SubjectID())
		if err := appendPair(deleteParent, deleteParentErr, touchParent, touchParentErr); err != nil {
			return nil, nil, fmt.Errorf("invalid label parent snapshot descriptor: %w", err)
		}

		var deleteMutation, restoreMutation policyv1.RelationshipMutation
		for _, pair := range pairs {
			if pair.Matches(snapshot) && (snapshot.ResourceID() == resourceID || snapshot.SubjectID() == resourceID) {
				deleteMutation, restoreMutation = pair.Delete(), pair.Restore()
				break
			}
		}
		if !deleteMutation.Valid() {
			return nil, nil, fmt.Errorf(
				"unsupported label relationship %s:%s#%s@%s:%s",
				snapshot.ResourceType(), snapshot.ResourceID(), snapshot.Relation(), snapshot.SubjectType(), snapshot.SubjectID(),
			)
		}
		deleted = append(deleted, deleteMutation)
		restored = append(restored, restoreMutation)
	}
	return deleted, restored, nil
}
