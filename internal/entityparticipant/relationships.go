package entityparticipant

import (
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// CreateRelationships returns the SpiceDB writes for a new entity: its policy,
// its first Owner and its optional parent. Compensation runs in reverse order.
func (s Spec) CreateRelationships(resourceID, ownerIdentityID string, parentID *string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	owner, err := policyv1.NewAccountIdentityActor(ownerIdentityID)
	if err != nil {
		return nil, nil, err
	}
	policyTouch, err := s.Policy.TouchPolicy(resourceID)
	if err != nil {
		return nil, nil, err
	}
	policyDelete, err := s.Policy.DeletePolicy(resourceID)
	if err != nil {
		return nil, nil, err
	}
	ownerTouch, err := s.Policy.TouchOwner(resourceID, owner)
	if err != nil {
		return nil, nil, err
	}
	ownerDelete, err := s.Policy.DeleteOwner(resourceID, owner)
	if err != nil {
		return nil, nil, err
	}
	apply := []policyv1.RelationshipMutation{policyTouch, ownerTouch}
	compensate := []policyv1.RelationshipMutation{ownerDelete, policyDelete}
	if parentID == nil {
		return apply, compensate, nil
	}
	parentTouch, err := s.Policy.TouchParent(resourceID, *parentID)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid %s parent relationship: %w", s.Name, err)
	}
	parentDelete, err := s.Policy.DeleteParent(resourceID, *parentID)
	if err != nil {
		return nil, nil, err
	}
	return append(apply, parentTouch), append([]policyv1.RelationshipMutation{parentDelete}, compensate...), nil
}

// ReplaceParentRelationships returns the SpiceDB writes that move the entity
// from previousParentID to nextParentID; either may be nil, not both.
func (s Spec) ReplaceParentRelationships(resourceID string, previousParentID, nextParentID *string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	apply := make([]policyv1.RelationshipMutation, 0, 2)
	compensate := make([]policyv1.RelationshipMutation, 0, 2)
	if previousParentID != nil {
		remove, err := s.Policy.DeleteParent(resourceID, *previousParentID)
		if err != nil {
			return nil, nil, err
		}
		restore, err := s.Policy.TouchParent(resourceID, *previousParentID)
		if err != nil {
			return nil, nil, err
		}
		apply = append(apply, remove)
		compensate = append(compensate, restore)
	}
	if nextParentID != nil {
		touch, err := s.Policy.TouchParent(resourceID, *nextParentID)
		if err != nil {
			return nil, nil, err
		}
		remove, err := s.Policy.DeleteParent(resourceID, *nextParentID)
		if err != nil {
			return nil, nil, err
		}
		apply = append(apply, touch)
		compensate = append([]policyv1.RelationshipMutation{remove}, compensate...)
	}
	if len(apply) == 0 {
		return nil, nil, fmt.Errorf("%s parent relationship change is required", s.Name)
	}
	return apply, compensate, nil
}

// DeletionRelationshipMutations maps the entity's SpiceDB relationship
// snapshot to deletes and the writes that restore them. A child's parent
// relationship points at the entity as its subject and is removed too.
func (s Spec) DeletionRelationshipMutations(resourceID string, snapshots []auth.RelationshipSnapshot) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	deleted := make([]policyv1.RelationshipMutation, 0, len(snapshots))
	restored := make([]policyv1.RelationshipMutation, 0, len(snapshots))
	for _, snapshot := range snapshots {
		pairs, err := s.snapshotMutationPairs(resourceID, snapshot)
		if err != nil {
			return nil, nil, err
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
				"unsupported %s relationship %s:%s#%s@%s:%s",
				s.Name, snapshot.ResourceType(), snapshot.ResourceID(), snapshot.Relation(), snapshot.SubjectType(), snapshot.SubjectID(),
			)
		}
		deleted = append(deleted, deleteMutation)
		restored = append(restored, restoreMutation)
	}
	return deleted, restored, nil
}

// snapshotMutationPairs lists every relationship the snapshot may be: the
// policy, an Owner or Manager when the subject is an account, and a parent.
func (s Spec) snapshotMutationPairs(resourceID string, snapshot auth.RelationshipSnapshot) ([]auth.RelationshipMutationPair, error) {
	pairs := make([]auth.RelationshipMutationPair, 0, 4)
	appendPair := func(kind string, deleteMutation policyv1.RelationshipMutation, deleteErr error, restoreMutation policyv1.RelationshipMutation, restoreErr error) error {
		if deleteErr == nil {
			deleteErr = restoreErr
		}
		if deleteErr != nil {
			return fmt.Errorf("invalid %s %s snapshot descriptor: %w", s.Name, kind, deleteErr)
		}
		pair, err := auth.NewRelationshipMutationPair(deleteMutation, restoreMutation)
		if err != nil {
			return fmt.Errorf("invalid %s %s snapshot descriptor: %w", s.Name, kind, err)
		}
		pairs = append(pairs, pair)
		return nil
	}
	deletePolicy, deletePolicyErr := s.Policy.DeletePolicy(resourceID)
	touchPolicy, touchPolicyErr := s.Policy.TouchPolicy(resourceID)
	if err := appendPair("policy", deletePolicy, deletePolicyErr, touchPolicy, touchPolicyErr); err != nil {
		return nil, err
	}
	if actor, err := policyv1.NewAccountIdentityActor(snapshot.SubjectID()); err == nil {
		deleteOwner, deleteOwnerErr := s.Policy.DeleteOwner(resourceID, actor)
		touchOwner, touchOwnerErr := s.Policy.TouchOwner(resourceID, actor)
		if err := appendPair("owner", deleteOwner, deleteOwnerErr, touchOwner, touchOwnerErr); err != nil {
			return nil, err
		}
		deleteManager, deleteManagerErr := s.Policy.DeleteManager(resourceID, actor)
		touchManager, touchManagerErr := s.Policy.TouchManager(resourceID, actor)
		if err := appendPair("manager", deleteManager, deleteManagerErr, touchManager, touchManagerErr); err != nil {
			return nil, err
		}
	}
	deleteParent, deleteParentErr := s.Policy.DeleteParent(snapshot.ResourceID(), snapshot.SubjectID())
	touchParent, touchParentErr := s.Policy.TouchParent(snapshot.ResourceID(), snapshot.SubjectID())
	if err := appendPair("parent", deleteParent, deleteParentErr, touchParent, touchParentErr); err != nil {
		return nil, err
	}
	return pairs, nil
}

// participantRelationshipMutations returns the SpiceDB writes that move an
// account from previousRole to nextRole, with compensation in reverse order.
func (s Spec) participantRelationshipMutations(entityID, identityID, previousRole, nextRole string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	if err != nil {
		return nil, nil, err
	}
	apply := make([]policyv1.RelationshipMutation, 0, 2)
	compensate := make([]policyv1.RelationshipMutation, 0, 2)
	if previousRole != RoleNone {
		mutation, err := s.deleteRole(entityID, actor, previousRole)
		if err != nil {
			return nil, nil, err
		}
		restore, err := s.touchRole(entityID, actor, previousRole)
		if err != nil {
			return nil, nil, err
		}
		apply = append(apply, mutation)
		compensate = append(compensate, restore)
	}
	if nextRole != RoleNone {
		mutation, err := s.touchRole(entityID, actor, nextRole)
		if err != nil {
			return nil, nil, err
		}
		remove, err := s.deleteRole(entityID, actor, nextRole)
		if err != nil {
			return nil, nil, err
		}
		apply = append(apply, mutation)
		compensate = append([]policyv1.RelationshipMutation{remove}, compensate...)
	}
	return apply, compensate, nil
}

func (s Spec) touchRole(entityID string, actor policyv1.Actor, role string) (policyv1.RelationshipMutation, error) {
	switch role {
	case RoleOwner:
		return s.Policy.TouchOwner(entityID, actor)
	case RoleManager:
		return s.Policy.TouchManager(entityID, actor)
	default:
		return policyv1.RelationshipMutation{}, fmt.Errorf("unsupported participant role %q", role)
	}
}

func (s Spec) deleteRole(entityID string, actor policyv1.Actor, role string) (policyv1.RelationshipMutation, error) {
	switch role {
	case RoleOwner:
		return s.Policy.DeleteOwner(entityID, actor)
	case RoleManager:
		return s.Policy.DeleteManager(entityID, actor)
	default:
		return policyv1.RelationshipMutation{}, fmt.Errorf("unsupported participant role %q", role)
	}
}
