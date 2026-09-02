package auth

import (
	"context"
	"io"
	"testing"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestExpectedRelationshipPreconditionsRequireExactInverseKeys(t *testing.T) {
	actor, err := policyv1.NewAccountIdentityActor("00000000-0000-4000-8000-000000000001")
	require.NoError(t, err)
	touchAuthor := testTranslatedRelationshipMutation(t, mustPostAuthorMutation(t, "post-1", actor, true))
	deleteAuthor := testTranslatedRelationshipMutation(t, mustPostAuthorMutation(t, "post-1", actor, false))
	deleteCollaborator := testTranslatedRelationshipMutation(t, mustPostCollaboratorMutation(t, "post-1", actor, false))
	touchCollaborator := testTranslatedRelationshipMutation(t, mustPostCollaboratorMutation(t, "post-1", actor, true))

	preconditions, states, err := expectedRelationshipPreconditions(
		[]relationshipMutation{touchAuthor, deleteCollaborator},
		[]relationshipMutation{deleteAuthor, touchCollaborator},
	)
	require.NoError(t, err)
	require.Len(t, states, 2)
	require.Len(t, preconditions, 2)
	require.Equal(t, v1.Precondition_OPERATION_MUST_NOT_MATCH, preconditions[0].GetOperation())
	require.Equal(t, v1.Precondition_OPERATION_MUST_MATCH, preconditions[1].GetOperation())
	require.Equal(t, "post", preconditions[0].GetFilter().GetResourceType())
	require.Equal(t, "post-1", preconditions[0].GetFilter().GetOptionalResourceId())
	require.Equal(t, "author", preconditions[0].GetFilter().GetOptionalRelation())
	require.Equal(t, "account_identity", preconditions[0].GetFilter().GetOptionalSubjectFilter().GetSubjectType())
	require.Equal(t, actor.AccountIdentityID(), preconditions[0].GetFilter().GetOptionalSubjectFilter().GetOptionalSubjectId())
	require.NotNil(t, preconditions[0].GetFilter().GetOptionalSubjectFilter().GetOptionalRelation())
	require.Empty(t, preconditions[0].GetFilter().GetOptionalSubjectFilter().GetOptionalRelation().GetRelation())

	_, _, err = expectedRelationshipPreconditions(
		[]relationshipMutation{touchAuthor},
		[]relationshipMutation{touchCollaborator},
	)
	require.ErrorContains(t, err, "no matching apply key")

	_, _, err = expectedRelationshipPreconditions(
		[]relationshipMutation{touchAuthor, touchAuthor},
		[]relationshipMutation{deleteAuthor, deleteAuthor},
	)
	require.ErrorContains(t, err, "duplicate key")

	preconditions, states, err = expectedRelationshipPreconditions(
		[]relationshipMutation{touchAuthor},
		[]relationshipMutation{touchAuthor},
	)
	require.NoError(t, err)
	require.Equal(t, v1.Precondition_OPERATION_MUST_MATCH, preconditions[0].GetOperation())
	require.True(t, states[0].desiredExists)
	require.True(t, states[0].preStateExists)
}

func TestExpectedRelationshipPreconditionIncludesRoleSubjectRelation(t *testing.T) {
	typedApply, err := policyv1.Platform.TouchAdminRole()
	require.NoError(t, err)
	typedCompensate, err := policyv1.Platform.DeleteAdminRole()
	require.NoError(t, err)
	apply := testTranslatedRelationshipMutation(t, typedApply)
	compensate := testTranslatedRelationshipMutation(t, typedCompensate)

	preconditions, _, err := expectedRelationshipPreconditions(
		[]relationshipMutation{apply},
		[]relationshipMutation{compensate},
	)
	require.NoError(t, err)
	require.Equal(t, "role", preconditions[0].GetFilter().GetOptionalSubjectFilter().GetSubjectType())
	require.Equal(t, typedApply.SubjectID(), preconditions[0].GetFilter().GetOptionalSubjectFilter().GetOptionalSubjectId())
	require.Equal(t, typedApply.SubjectRelation(), preconditions[0].GetFilter().GetOptionalSubjectFilter().GetOptionalRelation().GetRelation())
}

func TestConditionalRelationshipWriteResolvesAmbiguousDesiredState(t *testing.T) {
	apply, compensate := testPolicyRelationshipPair(t, "post-desired")
	fake := &fakePermissionsService{
		writeErrors:  []error{status.Error(codes.Unavailable, "response lost")},
		readRevision: "read-after-write",
		exists:       map[string]bool{testRelationshipKey(apply.update.GetRelationship()): true},
	}
	client := &SpiceDBClient{client: fake}

	token, err := client.writeRelationshipsExpecting(t.Context(), []relationshipMutation{apply}, []relationshipMutation{compensate})
	require.NoError(t, err)
	require.Equal(t, "read-after-write", token.String())
	require.Len(t, fake.writeRequests, 1)
	require.Len(t, fake.writeRequests[0].GetOptionalPreconditions(), 1)
}

func TestConditionalRelationshipWriteRetriesConditionalBatchWhenPreStateRemains(t *testing.T) {
	apply, compensate := testPolicyRelationshipPair(t, "post-retry")
	fake := &fakePermissionsService{
		writeErrors:    []error{status.Error(codes.DeadlineExceeded, "deadline"), nil},
		writeRevisions: []string{"", "written-after-retry"},
		readRevision:   "pre-state-read",
		exists:         map[string]bool{testRelationshipKey(apply.update.GetRelationship()): false},
	}
	client := &SpiceDBClient{client: fake}

	token, err := client.writeRelationshipsExpecting(t.Context(), []relationshipMutation{apply}, []relationshipMutation{compensate})
	require.NoError(t, err)
	require.Equal(t, "written-after-retry", token.String())
	require.Len(t, fake.writeRequests, 2)
	require.True(t, proto.Equal(fake.writeRequests[0], fake.writeRequests[1]))
}

func TestConditionalRelationshipWriteKeepsInitialPreconditionFailureStrict(t *testing.T) {
	apply, compensate := testPolicyRelationshipPair(t, "post-preexisting")
	fake := &fakePermissionsService{
		writeErrors:  []error{status.Error(codes.FailedPrecondition, "expected pre-state does not hold")},
		readRevision: "must-not-be-used",
		exists:       map[string]bool{testRelationshipKey(apply.update.GetRelationship()): true},
	}
	client := &SpiceDBClient{client: fake}

	_, err := client.writeRelationshipsExpecting(
		t.Context(),
		[]relationshipMutation{apply},
		[]relationshipMutation{compensate},
	)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.False(t, IsRelationshipWriteOutcomeUncertain(err))
	require.Len(t, fake.writeRequests, 1)
}

func TestConditionalRelationshipRestoreAcceptsAlreadyRestoredState(t *testing.T) {
	forward, restore := testPolicyRelationshipPair(t, "post-already-restored")
	fake := &fakePermissionsService{
		writeErrors:  []error{status.Error(codes.FailedPrecondition, "forward post-state is absent")},
		readRevision: "restored-read",
		exists:       map[string]bool{testRelationshipKey(forward.update.GetRelationship()): false},
	}
	client := &SpiceDBClient{client: fake}

	token, err := client.restoreRelationshipsExpecting(
		t.Context(),
		[]relationshipMutation{restore},
		[]relationshipMutation{forward},
	)
	require.NoError(t, err)
	require.Equal(t, "restored-read", token.String())
	require.Len(t, fake.writeRequests, 1)
	require.Equal(t, v1.Precondition_OPERATION_MUST_MATCH, fake.writeRequests[0].GetOptionalPreconditions()[0].GetOperation())
}

func TestConditionalRelationshipRestoreRejectsExpectedCurrentAndMixedStates(t *testing.T) {
	forward, restore := testPolicyRelationshipPair(t, "post-still-forward")
	fake := &fakePermissionsService{
		writeErrors:  []error{status.Error(codes.FailedPrecondition, "restore precondition")},
		readRevision: "forward-read",
		exists:       map[string]bool{testRelationshipKey(forward.update.GetRelationship()): true},
	}
	client := &SpiceDBClient{client: fake}

	_, err := client.restoreRelationshipsExpecting(
		t.Context(),
		[]relationshipMutation{restore},
		[]relationshipMutation{forward},
	)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.False(t, IsRelationshipWriteOutcomeUncertain(err))

	secondForward, secondRestore := testPolicyRelationshipPair(t, "post-restore-mixed")
	fake = &fakePermissionsService{
		writeErrors:  []error{status.Error(codes.FailedPrecondition, "restore precondition")},
		readRevision: "mixed-read",
		exists: map[string]bool{
			testRelationshipKey(forward.update.GetRelationship()):       false,
			testRelationshipKey(secondForward.update.GetRelationship()): true,
		},
	}
	client = &SpiceDBClient{client: fake}
	_, err = client.restoreRelationshipsExpecting(
		t.Context(),
		[]relationshipMutation{restore, secondRestore},
		[]relationshipMutation{forward, secondForward},
	)
	require.ErrorContains(t, err, "mixed or conflicting state")
	require.False(t, IsRelationshipWriteOutcomeUncertain(err))
}

func TestConditionalRelationshipWriteReportsConflictingAmbiguousState(t *testing.T) {
	firstApply, firstCompensate := testPolicyRelationshipPair(t, "post-mixed-1")
	secondApply, secondCompensate := testPolicyRelationshipPair(t, "post-mixed-2")
	fake := &fakePermissionsService{
		writeErrors:  []error{status.Error(codes.Unknown, "unknown")},
		readRevision: "mixed-read",
		exists: map[string]bool{
			testRelationshipKey(firstApply.update.GetRelationship()):  true,
			testRelationshipKey(secondApply.update.GetRelationship()): false,
		},
	}
	client := &SpiceDBClient{client: fake}

	_, err := client.writeRelationshipsExpecting(
		t.Context(),
		[]relationshipMutation{firstApply, secondApply},
		[]relationshipMutation{firstCompensate, secondCompensate},
	)
	require.Error(t, err)
	require.True(t, IsRelationshipWriteOutcomeUncertain(err))
	require.ErrorContains(t, err, "mixed or conflicting state")
	require.Len(t, fake.writeRequests, 1)
}

func TestConditionalRelationshipWriteKeepsExhaustedAmbiguousPreStateUncertain(t *testing.T) {
	apply, compensate := testPolicyRelationshipPair(t, "post-ambiguous-prestate")
	fake := &fakePermissionsService{
		writeErrors: []error{
			status.Error(codes.Unavailable, "one"),
			status.Error(codes.DeadlineExceeded, "two"),
			status.Error(codes.Unknown, "three"),
		},
		readRevision: "prestate-read",
		exists:       map[string]bool{testRelationshipKey(apply.update.GetRelationship()): false},
	}
	client := &SpiceDBClient{client: fake}

	_, err := client.writeRelationshipsExpecting(
		t.Context(),
		[]relationshipMutation{apply},
		[]relationshipMutation{compensate},
	)
	require.Error(t, err)
	require.True(t, IsRelationshipWriteOutcomeUncertain(err))
	require.Len(t, fake.writeRequests, relationshipWriteMaxAttempts)
}

func TestWriteRelationshipsRetriesAmbiguousIdempotentBatchAndBoundsFailure(t *testing.T) {
	apply, _ := testPolicyRelationshipPair(t, "post-idempotent")
	fake := &fakePermissionsService{
		writeErrors: []error{
			status.Error(codes.Unavailable, "one"),
			status.Error(codes.Internal, "two"),
			status.Error(codes.DeadlineExceeded, "three"),
		},
	}
	client := &SpiceDBClient{client: fake}

	_, err := client.writeRelationships(t.Context(), apply)
	require.Error(t, err)
	require.True(t, IsRelationshipWriteOutcomeUncertain(err))
	require.Len(t, fake.writeRequests, relationshipWriteMaxAttempts)
}

func testPolicyRelationshipPair(t *testing.T, resourceID string) (relationshipMutation, relationshipMutation) {
	t.Helper()
	typedApply, err := policyv1.Post.TouchPolicy(resourceID)
	require.NoError(t, err)
	typedCompensate, err := policyv1.Post.DeletePolicy(resourceID)
	require.NoError(t, err)
	return testTranslatedRelationshipMutation(t, typedApply), testTranslatedRelationshipMutation(t, typedCompensate)
}

func testTranslatedRelationshipMutation(t *testing.T, mutation policyv1.RelationshipMutation) relationshipMutation {
	t.Helper()
	translated, err := translateRelationshipMutation(mutation)
	require.NoError(t, err)
	return translated
}

func mustPostAuthorMutation(t *testing.T, postID string, actor policyv1.Actor, touch bool) policyv1.RelationshipMutation {
	t.Helper()
	var mutation policyv1.RelationshipMutation
	var err error
	if touch {
		mutation, err = policyv1.Post.TouchAuthor(postID, actor)
	} else {
		mutation, err = policyv1.Post.DeleteAuthor(postID, actor)
	}
	require.NoError(t, err)
	return mutation
}

func mustPostCollaboratorMutation(t *testing.T, postID string, actor policyv1.Actor, touch bool) policyv1.RelationshipMutation {
	t.Helper()
	var mutation policyv1.RelationshipMutation
	var err error
	if touch {
		mutation, err = policyv1.Post.TouchCollaborator(postID, actor)
	} else {
		mutation, err = policyv1.Post.DeleteCollaborator(postID, actor)
	}
	require.NoError(t, err)
	return mutation
}

type fakePermissionsService struct {
	v1.PermissionsServiceClient
	writeErrors    []error
	writeRevisions []string
	writeRequests  []*v1.WriteRelationshipsRequest
	readRevision   string
	exists         map[string]bool
}

func (fake *fakePermissionsService) WriteRelationships(
	_ context.Context,
	request *v1.WriteRelationshipsRequest,
	_ ...grpc.CallOption,
) (*v1.WriteRelationshipsResponse, error) {
	fake.writeRequests = append(fake.writeRequests, proto.Clone(request).(*v1.WriteRelationshipsRequest))
	index := len(fake.writeRequests) - 1
	if index < len(fake.writeErrors) && fake.writeErrors[index] != nil {
		return nil, fake.writeErrors[index]
	}
	revision := "written"
	if index < len(fake.writeRevisions) && fake.writeRevisions[index] != "" {
		revision = fake.writeRevisions[index]
	}
	return &v1.WriteRelationshipsResponse{WrittenAt: &v1.ZedToken{Token: revision}}, nil
}

func (fake *fakePermissionsService) CheckPermission(
	context.Context,
	*v1.CheckPermissionRequest,
	...grpc.CallOption,
) (*v1.CheckPermissionResponse, error) {
	return &v1.CheckPermissionResponse{CheckedAt: &v1.ZedToken{Token: fake.readRevision}}, nil
}

func (fake *fakePermissionsService) ReadRelationships(
	_ context.Context,
	request *v1.ReadRelationshipsRequest,
	_ ...grpc.CallOption,
) (grpc.ServerStreamingClient[v1.ReadRelationshipsResponse], error) {
	filter := request.GetRelationshipFilter()
	key := testRelationshipFilterKey(filter)
	stream := &fakeReadRelationshipsStream{}
	if fake.exists[key] {
		stream.responses = []*v1.ReadRelationshipsResponse{{Relationship: &v1.Relationship{
			Resource: &v1.ObjectReference{ObjectType: filter.GetResourceType(), ObjectId: filter.GetOptionalResourceId()},
			Relation: filter.GetOptionalRelation(),
			Subject: &v1.SubjectReference{
				Object: &v1.ObjectReference{
					ObjectType: filter.GetOptionalSubjectFilter().GetSubjectType(),
					ObjectId:   filter.GetOptionalSubjectFilter().GetOptionalSubjectId(),
				},
				OptionalRelation: filter.GetOptionalSubjectFilter().GetOptionalRelation().GetRelation(),
			},
		}}}
	}
	return stream, nil
}

type fakeReadRelationshipsStream struct {
	grpc.ServerStreamingClient[v1.ReadRelationshipsResponse]
	responses []*v1.ReadRelationshipsResponse
}

func (stream *fakeReadRelationshipsStream) Recv() (*v1.ReadRelationshipsResponse, error) {
	if len(stream.responses) == 0 {
		return nil, io.EOF
	}
	response := stream.responses[0]
	stream.responses = stream.responses[1:]
	return response, nil
}

func testRelationshipKey(relationship *v1.Relationship) string {
	return relationship.GetResource().GetObjectType() + "\x00" +
		relationship.GetResource().GetObjectId() + "\x00" +
		relationship.GetRelation() + "\x00" +
		relationship.GetSubject().GetObject().GetObjectType() + "\x00" +
		relationship.GetSubject().GetObject().GetObjectId() + "\x00" +
		relationship.GetSubject().GetOptionalRelation()
}

func testRelationshipFilterKey(filter *v1.RelationshipFilter) string {
	return filter.GetResourceType() + "\x00" +
		filter.GetOptionalResourceId() + "\x00" +
		filter.GetOptionalRelation() + "\x00" +
		filter.GetOptionalSubjectFilter().GetSubjectType() + "\x00" +
		filter.GetOptionalSubjectFilter().GetOptionalSubjectId() + "\x00" +
		filter.GetOptionalSubjectFilter().GetOptionalRelation().GetRelation()
}
