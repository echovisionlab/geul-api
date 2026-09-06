package artist

import (
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func createArtistRelationships(resourceID, ownerIdentityID string, parentID *string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	owner, err := policyv1.NewAccountIdentityActor(ownerIdentityID)
	if err != nil {
		return nil, nil, err
	}
	policyTouch, err := policyv1.Artist.TouchPolicy(resourceID)
	if err != nil {
		return nil, nil, err
	}
	policyDelete, err := policyv1.Artist.DeletePolicy(resourceID)
	if err != nil {
		return nil, nil, err
	}
	ownerTouch, err := policyv1.Artist.TouchOwner(resourceID, owner)
	if err != nil {
		return nil, nil, err
	}
	ownerDelete, err := policyv1.Artist.DeleteOwner(resourceID, owner)
	if err != nil {
		return nil, nil, err
	}
	apply := []policyv1.RelationshipMutation{policyTouch, ownerTouch}
	compensate := []policyv1.RelationshipMutation{ownerDelete, policyDelete}
	if parentID == nil {
		return apply, compensate, nil
	}
	parentTouch, err := policyv1.Artist.TouchParent(resourceID, *parentID)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid parent relationship: %w", err)
	}
	parentDelete, err := policyv1.Artist.DeleteParent(resourceID, *parentID)
	if err != nil {
		return nil, nil, err
	}
	return append(apply, parentTouch), append([]policyv1.RelationshipMutation{parentDelete}, compensate...), nil
}

func replaceArtistParentRelationships(resourceID string, previousParentID, nextParentID *string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	apply := make([]policyv1.RelationshipMutation, 0, 2)
	compensate := make([]policyv1.RelationshipMutation, 0, 2)
	if previousParentID != nil {
		remove, err := policyv1.Artist.DeleteParent(resourceID, *previousParentID)
		if err != nil {
			return nil, nil, err
		}
		restore, err := policyv1.Artist.TouchParent(resourceID, *previousParentID)
		if err != nil {
			return nil, nil, err
		}
		apply = append(apply, remove)
		compensate = append(compensate, restore)
	}
	if nextParentID != nil {
		touch, err := policyv1.Artist.TouchParent(resourceID, *nextParentID)
		if err != nil {
			return nil, nil, err
		}
		remove, err := policyv1.Artist.DeleteParent(resourceID, *nextParentID)
		if err != nil {
			return nil, nil, err
		}
		apply = append(apply, touch)
		compensate = append([]policyv1.RelationshipMutation{remove}, compensate...)
	}
	if len(apply) == 0 {
		return nil, nil, fmt.Errorf("parent relationship change is required")
	}
	return apply, compensate, nil
}

func artistDeletionRelationshipMutations(resourceID string, snapshots []auth.RelationshipSnapshot) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
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
		deletePolicy, deletePolicyErr := policyv1.Artist.DeletePolicy(resourceID)
		touchPolicy, touchPolicyErr := policyv1.Artist.TouchPolicy(resourceID)
		if err := appendPair(deletePolicy, deletePolicyErr, touchPolicy, touchPolicyErr); err != nil {
			return nil, nil, fmt.Errorf("invalid artist policy snapshot descriptor: %w", err)
		}
		actor, actorErr := policyv1.NewAccountIdentityActor(snapshot.SubjectID())
		if actorErr == nil {
			deleteOwner, deleteOwnerErr := policyv1.Artist.DeleteOwner(resourceID, actor)
			touchOwner, touchOwnerErr := policyv1.Artist.TouchOwner(resourceID, actor)
			if err := appendPair(deleteOwner, deleteOwnerErr, touchOwner, touchOwnerErr); err != nil {
				return nil, nil, fmt.Errorf("invalid artist owner snapshot descriptor: %w", err)
			}
			deleteManager, deleteManagerErr := policyv1.Artist.DeleteManager(resourceID, actor)
			touchManager, touchManagerErr := policyv1.Artist.TouchManager(resourceID, actor)
			if err := appendPair(deleteManager, deleteManagerErr, touchManager, touchManagerErr); err != nil {
				return nil, nil, fmt.Errorf("invalid artist manager snapshot descriptor: %w", err)
			}
		}
		deleteParent, deleteParentErr := policyv1.Artist.DeleteParent(snapshot.ResourceID(), snapshot.SubjectID())
		touchParent, touchParentErr := policyv1.Artist.TouchParent(snapshot.ResourceID(), snapshot.SubjectID())
		if err := appendPair(deleteParent, deleteParentErr, touchParent, touchParentErr); err != nil {
			return nil, nil, fmt.Errorf("invalid artist parent snapshot descriptor: %w", err)
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
				"unsupported artist relationship %s:%s#%s@%s:%s",
				snapshot.ResourceType(), snapshot.ResourceID(), snapshot.Relation(), snapshot.SubjectType(), snapshot.SubjectID(),
			)
		}
		deleted = append(deleted, deleteMutation)
		restored = append(restored, restoreMutation)
	}
	return deleted, restored, nil
}
