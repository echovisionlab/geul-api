package post

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

const maxSpiceDBAtomicRelationshipMutations = 1000

func validateResourceDeletionAuthorizationBatchSize(
	resourceName string,
	deleteRelationships, restoreRelationships []policyv1.RelationshipMutation,
) error {
	if len(deleteRelationships) <= maxSpiceDBAtomicRelationshipMutations &&
		len(restoreRelationships) <= maxSpiceDBAtomicRelationshipMutations {
		return nil
	}
	return errs.FailedPrecondition(fmt.Sprintf(
		"%s has too many authorization relationships to delete atomically; remove participant relationships or reparent dependent resources first",
		resourceName,
	))
}

func postAuthorizationDeletionBatches(
	postID string,
	snapshots []auth.RelationshipSnapshot,
) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	deletes := make([]policyv1.RelationshipMutation, 0, len(snapshots))
	restores := make([]policyv1.RelationshipMutation, 0, len(snapshots))
	for _, snapshot := range snapshots {
		pairs := make([]auth.RelationshipMutationPair, 0, 3)
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
		deletePolicy, deletePolicyErr := policyv1.Post.DeletePolicy(postID)
		touchPolicy, touchPolicyErr := policyv1.Post.TouchPolicy(postID)
		if err := appendPair(deletePolicy, deletePolicyErr, touchPolicy, touchPolicyErr); err != nil {
			return nil, nil, errs.Internal(err)
		}
		actor, actorErr := policyv1.NewAccountIdentityActor(snapshot.SubjectID())
		if actorErr == nil {
			deleteAuthor, deleteAuthorErr := policyv1.Post.DeleteAuthor(postID, actor)
			touchAuthor, touchAuthorErr := policyv1.Post.TouchAuthor(postID, actor)
			if err := appendPair(deleteAuthor, deleteAuthorErr, touchAuthor, touchAuthorErr); err != nil {
				return nil, nil, errs.Internal(err)
			}
			deleteCollaborator, deleteCollaboratorErr := policyv1.Post.DeleteCollaborator(postID, actor)
			touchCollaborator, touchCollaboratorErr := policyv1.Post.TouchCollaborator(postID, actor)
			if err := appendPair(deleteCollaborator, deleteCollaboratorErr, touchCollaborator, touchCollaboratorErr); err != nil {
				return nil, nil, errs.Internal(err)
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
			return nil, nil, errs.FailedPrecondition("post authorization relationships contain an unsupported tuple")
		}
		deletes = append(deletes, matched.Delete())
		restores = append(restores, matched.Restore())
	}
	return deletes, restores, nil
}

func cancelAndReleaseEntityOgWithDB(
	ctx context.Context,
	tx *gorm.DB,
	cdnDomain string,
	entityType managev1.OgEntityType,
	ownerType string,
	ownerID string,
) error {
	if err := og.NewLifecycle(tx, cdnDomain).CancelEntityWithDB(ctx, tx, entityType, ownerID); err != nil {
		return err
	}
	return mediaasset.NewLifecycle(tx, cdnDomain).ReleasePublicAssetBindings(ctx, ownerType, ownerID, "og")
}
