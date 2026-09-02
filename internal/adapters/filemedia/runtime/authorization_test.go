package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fileAuthorizationWriteCall struct {
	apply      []policyv1.RelationshipMutation
	compensate []policyv1.RelationshipMutation
}

type recordingFileAuthorizationStore struct {
	deleteMutations  []policyv1.RelationshipMutation
	restoreMutations []policyv1.RelationshipMutation
	writes           []fileAuthorizationWriteCall
	restores         []fileAuthorizationWriteCall
	verifiedToken    auth.ZedToken
	verifiedPlan     policyv1.RelationshipSnapshotPlan
	verifyErr        error
	writeErr         error
	panicForward     bool
}

func (store *recordingFileAuthorizationStore) ApplyRelationshipsExpecting(
	_ context.Context,
	apply []policyv1.RelationshipMutation,
	compensate []policyv1.RelationshipMutation,
) (auth.ZedToken, error) {
	store.writes = append(store.writes, fileAuthorizationWriteCall{
		apply:      append([]policyv1.RelationshipMutation(nil), apply...),
		compensate: append([]policyv1.RelationshipMutation(nil), compensate...),
	})
	if store.panicForward {
		panic("forward delete panic")
	}
	return auth.ZedToken{}, store.writeErr
}

func (store *recordingFileAuthorizationStore) CompensateRelationshipsExpecting(
	_ context.Context,
	apply []policyv1.RelationshipMutation,
	compensate []policyv1.RelationshipMutation,
) (auth.ZedToken, error) {
	store.restores = append(store.restores, fileAuthorizationWriteCall{
		apply:      append([]policyv1.RelationshipMutation(nil), apply...),
		compensate: append([]policyv1.RelationshipMutation(nil), compensate...),
	})
	return auth.ZedToken{}, nil
}

func (store *recordingFileAuthorizationStore) VerifyResourceRelationshipsDeleted(
	_ context.Context,
	plan policyv1.RelationshipSnapshotPlan,
	token auth.ZedToken,
) error {
	store.verifiedPlan = plan
	store.verifiedToken = token
	return store.verifyErr
}

func fileAuthorizationMutationPair(t *testing.T) (policyv1.Resource, policyv1.RelationshipMutation, policyv1.RelationshipMutation) {
	t.Helper()
	resource, err := policyv1.File.Resource(uuid.NewString())
	require.NoError(t, err)
	deleteMutation, err := policyv1.File.DeletePolicy(resource.ID())
	require.NoError(t, err)
	restoreMutation, err := policyv1.File.TouchPolicy(resource.ID())
	require.NoError(t, err)
	return resource, deleteMutation, restoreMutation
}

func newRecordingFileAuthorizationDeletion(
	store *recordingFileAuthorizationStore,
	isWriteOutcomeUncertain func(error) bool,
) *AuthorizationDeletion {
	return newAuthorizationDeletion(
		store,
		func(context.Context, policyv1.RelationshipSnapshotPlan) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, auth.ZedToken, error) {
			return store.deleteMutations, store.restoreMutations, auth.ZedToken{}, nil
		},
		isWriteOutcomeUncertain,
	)
}

type fileRelationshipSnapshot struct {
	resourceType    string
	resourceID      string
	relation        string
	subjectType     string
	subjectID       string
	subjectRelation string
}

func (snapshot fileRelationshipSnapshot) ResourceType() string    { return snapshot.resourceType }
func (snapshot fileRelationshipSnapshot) ResourceID() string      { return snapshot.resourceID }
func (snapshot fileRelationshipSnapshot) Relation() string        { return snapshot.relation }
func (snapshot fileRelationshipSnapshot) SubjectType() string     { return snapshot.subjectType }
func (snapshot fileRelationshipSnapshot) SubjectID() string       { return snapshot.subjectID }
func (snapshot fileRelationshipSnapshot) SubjectRelation() string { return snapshot.subjectRelation }

func fileRelationshipSnapshotFrom(mutation policyv1.RelationshipMutation) fileRelationshipSnapshot {
	return fileRelationshipSnapshot{
		resourceType:    mutation.Resource().Type(),
		resourceID:      mutation.Resource().ID(),
		relation:        mutation.Relation(),
		subjectType:     mutation.SubjectType(),
		subjectID:       mutation.SubjectID(),
		subjectRelation: mutation.SubjectRelation(),
	}
}

func TestFileAuthorizationMutationAcceptsOnlyGeneratedFilePolicyShape(t *testing.T) {
	resource, deleteMutation, restoreMutation := fileAuthorizationMutationPair(t)
	valid := fileRelationshipSnapshotFrom(deleteMutation)

	deletes, restores, err := fileAuthorizationMutation(resource, valid)
	require.NoError(t, err)
	require.Equal(t, []policyv1.RelationshipMutation{deleteMutation}, deletes)
	require.Equal(t, []policyv1.RelationshipMutation{restoreMutation}, restores)

	tests := map[string]fileRelationshipSnapshot{
		"resource type":    func() fileRelationshipSnapshot { value := valid; value.resourceType = "unknown"; return value }(),
		"resource id":      func() fileRelationshipSnapshot { value := valid; value.resourceID = uuid.NewString(); return value }(),
		"relation":         func() fileRelationshipSnapshot { value := valid; value.relation = "unknown"; return value }(),
		"subject type":     func() fileRelationshipSnapshot { value := valid; value.subjectType = "unknown"; return value }(),
		"subject id":       func() fileRelationshipSnapshot { value := valid; value.subjectID = "unknown"; return value }(),
		"subject relation": func() fileRelationshipSnapshot { value := valid; value.subjectRelation = "member"; return value }(),
	}
	for name, snapshot := range tests {
		t.Run(name, func(t *testing.T) {
			deletes, restores, err := fileAuthorizationMutation(resource, snapshot)
			require.ErrorContains(t, err, "outside the generated File policy contract")
			require.Nil(t, deletes)
			require.Nil(t, restores)
		})
	}
}

func TestSpiceDBFileAuthorizationDeletionUsesExactSnapshotBatchAndReturnedToken(t *testing.T) {
	resource, deleteMutation, restoreMutation := fileAuthorizationMutationPair(t)
	store := &recordingFileAuthorizationStore{
		deleteMutations:  []policyv1.RelationshipMutation{deleteMutation},
		restoreMutations: []policyv1.RelationshipMutation{restoreMutation},
	}
	deletion := newRecordingFileAuthorizationDeletion(store, nil)
	plan, err := policyv1.File.Snapshot(resource.ID())
	require.NoError(t, err)

	restore, confirmedAt, err := deletion.DeleteAndVerify(t.Context(), resource)
	require.NoError(t, err)
	require.False(t, confirmedAt.IsZero())
	require.Len(t, store.writes, 1)
	require.Equal(t, store.deleteMutations, store.writes[0].apply)
	require.Equal(t, store.restoreMutations, store.writes[0].compensate)
	require.Equal(t, plan, store.verifiedPlan)
	require.Equal(t, auth.ZedToken{}, store.verifiedToken)

	require.NoError(t, restore(t.Context()))
	require.Len(t, store.restores, 1)
	require.Equal(t, store.restoreMutations, store.restores[0].apply)
	require.Equal(t, store.deleteMutations, store.restores[0].compensate)
}

func TestSpiceDBFileAuthorizationDeletionRestoresExactBatchAfterVerificationFailure(t *testing.T) {
	resource, deleteMutation, restoreMutation := fileAuthorizationMutationPair(t)
	store := &recordingFileAuthorizationStore{
		deleteMutations:  []policyv1.RelationshipMutation{deleteMutation},
		restoreMutations: []policyv1.RelationshipMutation{restoreMutation},
		verifyErr:        errors.New("verification unavailable"),
	}
	deletion := newRecordingFileAuthorizationDeletion(store, nil)

	restore, confirmedAt, err := deletion.DeleteAndVerify(t.Context(), resource)
	require.ErrorContains(t, err, "verify File authorization relationships deleted")
	require.Nil(t, restore)
	require.True(t, confirmedAt.IsZero())
	require.Len(t, store.writes, 1)
	require.Len(t, store.restores, 1)
	require.Equal(t, store.deleteMutations, store.writes[0].apply)
	require.Equal(t, store.restoreMutations, store.restores[0].apply)
}

func TestSpiceDBFileAuthorizationDeletionRestoresExactBatchAfterUncertainWrite(t *testing.T) {
	resource, deleteMutation, restoreMutation := fileAuthorizationMutationPair(t)
	writeErr := errors.New("write result unavailable")
	store := &recordingFileAuthorizationStore{
		deleteMutations:  []policyv1.RelationshipMutation{deleteMutation},
		restoreMutations: []policyv1.RelationshipMutation{restoreMutation},
		writeErr:         writeErr,
	}
	deletion := newRecordingFileAuthorizationDeletion(
		store,
		func(err error) bool {
			return errors.Is(err, writeErr)
		},
	)

	restore, confirmedAt, err := deletion.DeleteAndVerify(t.Context(), resource)
	require.ErrorIs(t, err, writeErr)
	require.Nil(t, restore)
	require.True(t, confirmedAt.IsZero())
	require.Len(t, store.writes, 1)
	require.Len(t, store.restores, 1)
	require.Equal(t, store.restoreMutations, store.restores[0].apply)
	require.Equal(t, store.deleteMutations, store.restores[0].compensate)
}

func TestSpiceDBFileAuthorizationDeletionPanicRestoresExactPreState(t *testing.T) {
	resource, deleteMutation, restoreMutation := fileAuthorizationMutationPair(t)
	store := &recordingFileAuthorizationStore{
		deleteMutations:  []policyv1.RelationshipMutation{deleteMutation},
		restoreMutations: []policyv1.RelationshipMutation{restoreMutation},
		panicForward:     true,
	}
	deletion := newRecordingFileAuthorizationDeletion(store, nil)

	panicValue := captureWorkerTransactionPanic(func() {
		_, _, _ = deletion.DeleteAndVerify(t.Context(), resource)
	})
	require.Equal(t, "forward delete panic", panicValue)
	require.Len(t, store.writes, 1)
	require.Len(t, store.restores, 1)
	require.Equal(t, store.restoreMutations, store.restores[0].apply)
	require.Equal(t, store.deleteMutations, store.restores[0].compensate)
}

func captureWorkerTransactionPanic(run func()) (panicValue any) {
	defer func() { panicValue = recover() }()
	run()
	return nil
}
