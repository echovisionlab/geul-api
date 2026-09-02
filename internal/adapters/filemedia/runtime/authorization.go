package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type AuthorizationRelationshipStore interface {
	SnapshotResourceRelationshipDescriptors(context.Context, policyv1.RelationshipSnapshotPlan) ([]auth.RelationshipSnapshot, auth.ZedToken, error)
	authorizationRelationshipMutationStore
}

type authorizationRelationshipMutationStore interface {
	ApplyRelationshipsExpecting(context.Context, []policyv1.RelationshipMutation, []policyv1.RelationshipMutation) (auth.ZedToken, error)
	CompensateRelationshipsExpecting(context.Context, []policyv1.RelationshipMutation, []policyv1.RelationshipMutation) (auth.ZedToken, error)
	VerifyResourceRelationshipsDeleted(context.Context, policyv1.RelationshipSnapshotPlan, auth.ZedToken) error
}

type fileAuthorizationSnapshot func(
	context.Context,
	policyv1.RelationshipSnapshotPlan,
) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, auth.ZedToken, error)

type AuthorizationDeletion struct {
	client                  authorizationRelationshipMutationStore
	snapshot                fileAuthorizationSnapshot
	isWriteOutcomeUncertain func(error) bool
}

func NewAuthorizationDeletion(
	client AuthorizationRelationshipStore,
	isWriteOutcomeUncertain func(error) bool,
) *AuthorizationDeletion {
	return newAuthorizationDeletion(
		client,
		func(ctx context.Context, plan policyv1.RelationshipSnapshotPlan) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, auth.ZedToken, error) {
			snapshots, readAt, err := client.SnapshotResourceRelationshipDescriptors(ctx, plan)
			if err != nil {
				return nil, nil, auth.ZedToken{}, err
			}
			deleteMutations, restoreMutations, err := fileAuthorizationMutations(plan.Resource(), snapshots)
			return deleteMutations, restoreMutations, readAt, err
		},
		isWriteOutcomeUncertain,
	)
}

func newAuthorizationDeletion(
	client authorizationRelationshipMutationStore,
	snapshot fileAuthorizationSnapshot,
	isWriteOutcomeUncertain func(error) bool,
) *AuthorizationDeletion {
	return &AuthorizationDeletion{
		client:                  client,
		snapshot:                snapshot,
		isWriteOutcomeUncertain: isWriteOutcomeUncertain,
	}
}

func (d *AuthorizationDeletion) writeOutcomeIsUncertain(err error) bool {
	if d.isWriteOutcomeUncertain != nil {
		return d.isWriteOutcomeUncertain(err)
	}
	return auth.IsRelationshipWriteOutcomeUncertain(err)
}

func (d *AuthorizationDeletion) DeleteAndVerify(
	ctx context.Context,
	resource policyv1.Resource,
) (func(context.Context) error, time.Time, error) {
	if d == nil || d.client == nil || d.snapshot == nil {
		return nil, time.Time{}, fmt.Errorf("SpiceDB client is required")
	}
	plan, err := policyv1.File.Snapshot(resource.ID())
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("construct File authorization snapshot: %w", err)
	}
	deleteMutations, restoreMutations, _, err := d.snapshot(ctx, plan)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("snapshot File authorization relationships: %w", err)
	}
	restore := func(restoreCtx context.Context) error {
		_, err := d.client.CompensateRelationshipsExpecting(restoreCtx, restoreMutations, deleteMutations)
		return err
	}
	restoreBounded := func() error {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := restore(restoreCtx); err != nil {
			authzmutation.RecordAuthorizationRollbackCompensationFailed(restoreCtx)
			return err
		}
		return nil
	}
	forwardMayHaveApplied := false
	defer func() {
		panicValue := recover()
		if panicValue == nil {
			return
		}
		if !forwardMayHaveApplied {
			panic(panicValue)
		}
		if restoreErr := restoreBounded(); restoreErr != nil {
			panic(errors.Join(
				panicError(panicValue),
				fmt.Errorf("restore File authorization relationships after panic: %w", restoreErr),
			))
		}
		panic(panicValue)
	}()

	forwardMayHaveApplied = true
	deletedAt, err := d.client.ApplyRelationshipsExpecting(ctx, deleteMutations, restoreMutations)
	if err != nil {
		if !d.writeOutcomeIsUncertain(err) {
			forwardMayHaveApplied = false
			return nil, time.Time{}, fmt.Errorf("delete File authorization relationships: %w", err)
		}
		restoreErr := restoreBounded()
		if restoreErr == nil {
			forwardMayHaveApplied = false
		}
		return nil, time.Time{}, errors.Join(
			fmt.Errorf("delete File authorization relationships: %w", err),
			wrapAuthorizationRestoreError(restoreErr, " after uncertain delete"),
		)
	}
	confirmedAt := time.Now()
	if err := d.client.VerifyResourceRelationshipsDeleted(ctx, plan, deletedAt); err != nil {
		if restoreErr := restoreBounded(); restoreErr != nil {
			return nil, time.Time{}, errors.Join(
				fmt.Errorf("verify File authorization relationships deleted: %w", err),
				fmt.Errorf("restore File authorization relationships after failed verification: %w", restoreErr),
			)
		}
		forwardMayHaveApplied = false
		return nil, time.Time{}, fmt.Errorf("verify File authorization relationships deleted: %w", err)
	}
	forwardMayHaveApplied = false
	return restore, confirmedAt, nil
}

func fileAuthorizationMutations(
	resource policyv1.Resource,
	snapshots []auth.RelationshipSnapshot,
) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	if len(snapshots) == 0 {
		return nil, nil, fmt.Errorf("exact policy pre-state is required")
	}
	if len(snapshots) != 1 {
		return nil, nil, fmt.Errorf("observed relationship is outside the generated File policy contract")
	}
	return fileAuthorizationMutation(resource, snapshots[0])
}

type relationshipSnapshotDescriptor interface {
	ResourceType() string
	ResourceID() string
	Relation() string
	SubjectType() string
	SubjectID() string
	SubjectRelation() string
}

func fileAuthorizationMutation(
	resource policyv1.Resource,
	snapshot relationshipSnapshotDescriptor,
) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	if !resource.Valid() {
		return nil, nil, fmt.Errorf("file resource descriptor is invalid")
	}
	expectedResource, err := policyv1.File.Resource(resource.ID())
	if err != nil {
		return nil, nil, fmt.Errorf("build File resource descriptor: %w", err)
	}
	if resource.Type() != expectedResource.Type() || resource.ID() != expectedResource.ID() {
		return nil, nil, fmt.Errorf("resource is outside the generated File contract")
	}
	deleteMutation, err := policyv1.File.DeletePolicy(resource.ID())
	if err != nil {
		return nil, nil, fmt.Errorf("build File policy deletion: %w", err)
	}
	restoreMutation, err := policyv1.File.TouchPolicy(resource.ID())
	if err != nil {
		return nil, nil, fmt.Errorf("build File policy restoration: %w", err)
	}
	if snapshot == nil || !snapshotMatchesGeneratedMutation(snapshot, deleteMutation) {
		return nil, nil, fmt.Errorf("observed relationship is outside the generated File policy contract")
	}
	return []policyv1.RelationshipMutation{deleteMutation}, []policyv1.RelationshipMutation{restoreMutation}, nil
}

func snapshotMatchesGeneratedMutation(snapshot relationshipSnapshotDescriptor, expected policyv1.RelationshipMutation) bool {
	return snapshot.ResourceType() == expected.Resource().Type() &&
		snapshot.ResourceID() == expected.Resource().ID() &&
		snapshot.Relation() == expected.Relation() &&
		snapshot.SubjectType() == expected.SubjectType() &&
		snapshot.SubjectID() == expected.SubjectID() &&
		snapshot.SubjectRelation() == expected.SubjectRelation()
}

func wrapAuthorizationRestoreError(err error, suffix string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("restore File authorization relationships%s: %w", suffix, err)
}

func panicError(value any) error {
	if err, ok := value.(error); ok {
		return err
	}
	return fmt.Errorf("file authorization panic: %v", value)
}
