package auth

import (
	"context"
	"io"
	"testing"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

const typedTestAccountIdentityID = "00000000-0000-4000-8000-000000000001"

func TestCanUsesOneIdenticalEngineCheckForDirectAndMCPDelegation(t *testing.T) {
	actor, err := policyv1.NewAccountIdentityActor(typedTestAccountIdentityID)
	require.NoError(t, err)
	can, err := policyv1.Post.Edit("post-1")
	require.NoError(t, err)
	direct, err := policyv1.DirectSession("session-1")
	require.NoError(t, err)
	mcp, err := policyv1.MCPOAuth("credential-1", "Example Member · Example Client")
	require.NoError(t, err)
	directDecision, err := policyv1.NewAuthorizationDecision(actor, direct, can)
	require.NoError(t, err)
	mcpDecision, err := policyv1.NewAuthorizationDecision(actor, mcp, can)
	require.NoError(t, err)
	require.Equal(t, directDecision.EngineKey(), mcpDecision.EngineKey())

	fake := &typedPermissionsService{
		checkResponse: &v1.CheckPermissionResponse{
			Permissionship: v1.CheckPermissionResponse_PERMISSIONSHIP_HAS_PERMISSION,
			CheckedAt:      &v1.ZedToken{Token: "typed-check"},
		},
	}
	client := &SpiceDBClient{client: fake}

	allowed, err := client.Can(t.Context(), directDecision)
	require.NoError(t, err)
	require.True(t, allowed)
	require.Len(t, fake.checkRequests, 1)

	allowed, err = client.Can(t.Context(), mcpDecision)
	require.NoError(t, err)
	require.True(t, allowed)
	require.Len(t, fake.checkRequests, 2)
	require.True(t, proto.Equal(fake.checkRequests[0], fake.checkRequests[1]))

	request := fake.checkRequests[0]
	require.Equal(t, "post", request.GetResource().GetObjectType())
	require.Equal(t, "post-1", request.GetResource().GetObjectId())
	require.Equal(t, "edit", request.GetPermission())
	require.Equal(t, spiceDBAccountIdentityObjectType, request.GetSubject().GetObject().GetObjectType())
	require.Equal(t, typedTestAccountIdentityID, request.GetSubject().GetObject().GetObjectId())
	require.True(t, request.GetConsistency().GetFullyConsistent())
}

func TestCanRejectsInvalidDecisionBeforeSpiceDBIO(t *testing.T) {
	fake := &typedPermissionsService{}
	client := &SpiceDBClient{client: fake}

	allowed, err := client.Can(t.Context(), policyv1.AuthorizationDecision{})
	require.ErrorContains(t, err, "authorization decision is invalid")
	require.False(t, allowed)
	require.Empty(t, fake.checkRequests)

	actor, err := policyv1.NewAccountIdentityActor("not-a-uuid")
	require.NoError(t, err)
	delegation, err := policyv1.DirectSession("session-1")
	require.NoError(t, err)
	can, err := policyv1.Post.Edit("post-1")
	require.NoError(t, err)
	decision, err := policyv1.NewAuthorizationDecision(actor, delegation, can)
	require.NoError(t, err)

	allowed, err = client.Can(t.Context(), decision)
	require.ErrorContains(t, err, "authorization decision: actor")
	require.False(t, allowed)
	require.Empty(t, fake.checkRequests)
}

func TestCheckActorCanUsesSameEngineRequestWithoutClaimingDelegation(t *testing.T) {
	actor, err := policyv1.NewAccountIdentityActor(typedTestAccountIdentityID)
	require.NoError(t, err)
	can, err := policyv1.Post.Manage("post-1")
	require.NoError(t, err)
	fake := &typedPermissionsService{checkResponse: &v1.CheckPermissionResponse{
		Permissionship: v1.CheckPermissionResponse_PERMISSIONSHIP_HAS_PERMISSION,
	}}
	client := &SpiceDBClient{client: fake}

	allowed, err := client.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	require.True(t, allowed)
	require.Len(t, fake.checkRequests, 1)

	delegation, err := policyv1.DirectSession("session-1")
	require.NoError(t, err)
	decision, err := policyv1.NewAuthorizationDecision(actor, delegation, can)
	require.NoError(t, err)
	allowed, err = client.Can(t.Context(), decision)
	require.NoError(t, err)
	require.True(t, allowed)
	require.Len(t, fake.checkRequests, 2)
	require.True(t, proto.Equal(fake.checkRequests[0], fake.checkRequests[1]))
}

func TestCheckActorCanRejectsInvalidDescriptorsBeforeSpiceDBIO(t *testing.T) {
	fake := &typedPermissionsService{}
	client := &SpiceDBClient{client: fake}
	actor, err := policyv1.NewAccountIdentityActor(typedTestAccountIdentityID)
	require.NoError(t, err)
	can, err := policyv1.Post.Manage("post-1")
	require.NoError(t, err)

	allowed, err := client.CheckActorCan(t.Context(), actor, policyv1.Can{})
	require.ErrorContains(t, err, "Can descriptor is invalid")
	require.False(t, allowed)
	require.Empty(t, fake.checkRequests)

	allowed, err = client.CheckActorCan(t.Context(), policyv1.Actor{}, can)
	require.ErrorContains(t, err, "actor, resource, and action descriptors are required")
	require.False(t, allowed)
	require.Empty(t, fake.checkRequests)

	invalidActor, err := policyv1.NewAccountIdentityActor("not-a-uuid")
	require.NoError(t, err)
	allowed, err = client.CheckActorCan(t.Context(), invalidActor, can)
	require.ErrorContains(t, err, "actor capability check: actor")
	require.False(t, allowed)
	require.Empty(t, fake.checkRequests)
}

func TestLookupResourcesUsesOneGeneratedLookupForAccountIdentityActor(t *testing.T) {
	actor, err := policyv1.NewAccountIdentityActor(typedTestAccountIdentityID)
	require.NoError(t, err)
	fake := &typedPermissionsService{lookupResponses: []*v1.LookupResourcesResponse{
		{ResourceObjectId: "artist-1"},
		{ResourceObjectId: "artist-2"},
	}}
	client := &SpiceDBClient{client: fake}

	resources, err := client.LookupResources(t.Context(), policyv1.Artist.LookupManage(), actor)
	require.NoError(t, err)
	require.Equal(t, []string{"artist-1", "artist-2"}, resources)
	require.Len(t, fake.lookupRequests, 1)

	request := fake.lookupRequests[0]
	require.Equal(t, "artist", request.GetResourceObjectType())
	require.Equal(t, "manage", request.GetPermission())
	require.Equal(t, spiceDBAccountIdentityObjectType, request.GetSubject().GetObject().GetObjectType())
	require.Equal(t, typedTestAccountIdentityID, request.GetSubject().GetObject().GetObjectId())
	require.True(t, request.GetConsistency().GetFullyConsistent())
}

func TestLookupResourcesRejectsInvalidDescriptorAndActorBeforeSpiceDBIO(t *testing.T) {
	fake := &typedPermissionsService{}
	client := &SpiceDBClient{client: fake}
	actor, err := policyv1.NewAccountIdentityActor(typedTestAccountIdentityID)
	require.NoError(t, err)

	resources, err := client.LookupResources(t.Context(), policyv1.ResourceLookup{}, actor)
	require.ErrorContains(t, err, "resource lookup descriptor is invalid")
	require.Nil(t, resources)
	require.Empty(t, fake.lookupRequests)

	resources, err = client.LookupResources(t.Context(), policyv1.Artist.LookupManage(), policyv1.Actor{})
	require.ErrorContains(t, err, "resource lookup actor is invalid")
	require.Nil(t, resources)
	require.Empty(t, fake.lookupRequests)

	invalidActor, err := policyv1.NewAccountIdentityActor("not-a-uuid")
	require.NoError(t, err)
	resources, err = client.LookupResources(t.Context(), policyv1.Artist.LookupManage(), invalidActor)
	require.ErrorContains(t, err, "resource lookup actor")
	require.Nil(t, resources)
	require.Empty(t, fake.lookupRequests)
}

func TestLookupGlobalSubjectsUsesGeneratedPlatformSelector(t *testing.T) {
	fake := &typedPermissionsService{lookupSubjectResponses: []*v1.LookupSubjectsResponse{
		{Subject: &v1.ResolvedSubject{SubjectObjectId: typedTestAccountIdentityID}},
	}}
	client := &SpiceDBClient{client: fake}

	subjects, err := client.LookupGlobalSubjects(t.Context(), policyv1.Platform.LookupAuthorSubjects())
	require.NoError(t, err)
	require.Len(t, subjects, 1)
	require.Equal(t, IdentityID(typedTestAccountIdentityID), subjects[0].ID)
	require.Len(t, fake.lookupSubjectRequests, 1)

	request := fake.lookupSubjectRequests[0]
	require.Equal(t, "platform", request.GetResource().GetObjectType())
	require.Equal(t, "global", request.GetResource().GetObjectId())
	require.Equal(t, "is_author", request.GetPermission())
	require.Equal(t, spiceDBAccountIdentityObjectType, request.GetSubjectObjectType())
	require.True(t, request.GetConsistency().GetFullyConsistent())
}

func TestLookupGlobalSubjectsRejectsInvalidDescriptorBeforeSpiceDBIO(t *testing.T) {
	fake := &typedPermissionsService{}
	client := &SpiceDBClient{client: fake}

	subjects, err := client.LookupGlobalSubjects(t.Context(), policyv1.SubjectLookup{})
	require.ErrorContains(t, err, "global subject lookup descriptor is invalid")
	require.Nil(t, subjects)
	require.Empty(t, fake.lookupSubjectRequests)
}

func TestApplyRelationshipsTranslatesClosedBatchIntoOneSpiceDBWrite(t *testing.T) {
	actor, err := policyv1.NewAccountIdentityActor(typedTestAccountIdentityID)
	require.NoError(t, err)
	author, err := policyv1.Post.TouchAuthor("post-1", actor)
	require.NoError(t, err)
	policy, err := policyv1.Post.TouchPolicy("post-1")
	require.NoError(t, err)
	parent, err := policyv1.Artist.TouchParent("artist-1", "artist-2")
	require.NoError(t, err)
	roleMember, err := policyv1.Role.TouchMember(policyv1.Role.Author(), actor)
	require.NoError(t, err)
	platformRole, err := policyv1.Platform.TouchAuthorRole()
	require.NoError(t, err)

	fake := &typedPermissionsService{writeToken: "typed-write"}
	client := &SpiceDBClient{client: fake}
	token, err := client.ApplyRelationships(t.Context(), author, policy, parent, roleMember, platformRole)
	require.NoError(t, err)
	require.Equal(t, "typed-write", token.String())
	require.Len(t, fake.writeRequests, 1)
	require.Len(t, fake.writeRequests[0].GetUpdates(), 5)

	authorUpdate := fake.writeRequests[0].GetUpdates()[0]
	require.Equal(t, v1.RelationshipUpdate_OPERATION_TOUCH, authorUpdate.GetOperation())
	require.Equal(t, "post", authorUpdate.GetRelationship().GetResource().GetObjectType())
	require.Equal(t, "author", authorUpdate.GetRelationship().GetRelation())
	require.Equal(t, spiceDBAccountIdentityObjectType, authorUpdate.GetRelationship().GetSubject().GetObject().GetObjectType())

	roleUpdate := fake.writeRequests[0].GetUpdates()[3]
	require.Equal(t, "role", roleUpdate.GetRelationship().GetResource().GetObjectType())
	require.Equal(t, "author", roleUpdate.GetRelationship().GetResource().GetObjectId())
	require.Equal(t, "member", roleUpdate.GetRelationship().GetRelation())

	platformUpdate := fake.writeRequests[0].GetUpdates()[4]
	require.Equal(t, "platform", platformUpdate.GetRelationship().GetResource().GetObjectType())
	require.Equal(t, "role", platformUpdate.GetRelationship().GetSubject().GetObject().GetObjectType())
	require.Equal(t, "member", platformUpdate.GetRelationship().GetSubject().GetOptionalRelation())
}

func TestApplyRelationshipsRejectsInvalidDescriptorsBeforeSpiceDBIO(t *testing.T) {
	fake := &typedPermissionsService{writeToken: "must-not-be-used"}
	client := &SpiceDBClient{client: fake}

	valid, err := policyv1.Post.TouchPolicy("post-1")
	require.NoError(t, err)
	_, err = client.ApplyRelationships(t.Context(), valid, policyv1.RelationshipMutation{})
	require.ErrorContains(t, err, "descriptor is invalid")
	require.Empty(t, fake.writeRequests)

	invalidActor, err := policyv1.NewAccountIdentityActor("not-a-uuid")
	require.NoError(t, err)
	mutation, err := policyv1.Post.TouchAuthor("post-1", invalidActor)
	require.NoError(t, err)
	_, err = client.ApplyRelationships(t.Context(), mutation)
	require.ErrorContains(t, err, "account identity id")
	require.Empty(t, fake.writeRequests)

	_, err = client.ApplyRelationships(t.Context())
	require.ErrorContains(t, err, "at least one relationship mutation")
	require.Empty(t, fake.writeRequests)
}

func TestApplyRelationshipsExpectingValidatesBothBatchesBeforeOneWrite(t *testing.T) {
	apply, err := policyv1.Post.TouchPolicy("post-1")
	require.NoError(t, err)
	compensate, err := policyv1.Post.DeletePolicy("post-1")
	require.NoError(t, err)
	fake := &typedPermissionsService{writeToken: "typed-conditional-write"}
	client := &SpiceDBClient{client: fake}

	token, err := client.ApplyRelationshipsExpecting(
		t.Context(),
		[]policyv1.RelationshipMutation{apply},
		[]policyv1.RelationshipMutation{compensate},
	)
	require.NoError(t, err)
	require.Equal(t, "typed-conditional-write", token.String())
	require.Len(t, fake.writeRequests, 1)
	require.Len(t, fake.writeRequests[0].GetUpdates(), 1)
	require.Len(t, fake.writeRequests[0].GetOptionalPreconditions(), 1)

	fake.writeRequests = nil
	_, err = client.ApplyRelationshipsExpecting(
		t.Context(),
		[]policyv1.RelationshipMutation{apply},
		[]policyv1.RelationshipMutation{{}},
	)
	require.ErrorContains(t, err, "compensate relationships")
	require.Empty(t, fake.writeRequests)
}

func TestSnapshotResourceRelationshipDescriptorsReturnsReadOnlyObservedTuple(t *testing.T) {
	fake := &typedPermissionsService{
		checkResponse: &v1.CheckPermissionResponse{CheckedAt: &v1.ZedToken{Token: "snapshot-revision"}},
		readResponseBatches: [][]*v1.ReadRelationshipsResponse{{{
			Relationship: &v1.Relationship{
				Resource: &v1.ObjectReference{ObjectType: "member", ObjectId: "member-1"},
				Relation: "policy",
				Subject: &v1.SubjectReference{Object: &v1.ObjectReference{
					ObjectType: "platform",
					ObjectId:   "global",
				}},
			},
		}}},
	}
	client := &SpiceDBClient{client: fake}
	plan, err := policyv1.Member.Snapshot("member-1")
	require.NoError(t, err)

	snapshots, readAt, err := client.SnapshotResourceRelationshipDescriptors(t.Context(), plan)
	require.NoError(t, err)
	require.Equal(t, "snapshot-revision", readAt.String())
	require.Len(t, snapshots, 1)
	require.Equal(t, "member", snapshots[0].ResourceType())
	require.Equal(t, "member-1", snapshots[0].ResourceID())
	require.Equal(t, "policy", snapshots[0].Relation())
	require.Equal(t, "platform", snapshots[0].SubjectType())
	require.Equal(t, "global", snapshots[0].SubjectID())
	require.Empty(t, snapshots[0].SubjectRelation())
	require.Len(t, fake.checkRequests, 1)
	require.Len(t, fake.readRequests, 1)
}

func TestSnapshotResourceRelationshipDescriptorsRejectsInvalidResourceBeforeSpiceDBIO(t *testing.T) {
	fake := &typedPermissionsService{}
	client := &SpiceDBClient{client: fake}

	snapshots, readAt, err := client.SnapshotResourceRelationshipDescriptors(t.Context(), policyv1.RelationshipSnapshotPlan{})
	require.ErrorContains(t, err, "resource relationship snapshot plan is invalid")
	require.Nil(t, snapshots)
	require.Empty(t, readAt.String())
	require.Empty(t, fake.checkRequests)
	require.Empty(t, fake.readRequests)
}

type typedPermissionsService struct {
	v1.PermissionsServiceClient
	checkResponse          *v1.CheckPermissionResponse
	checkError             error
	checkRequests          []*v1.CheckPermissionRequest
	lookupResponses        []*v1.LookupResourcesResponse
	lookupError            error
	lookupRequests         []*v1.LookupResourcesRequest
	lookupSubjectResponses []*v1.LookupSubjectsResponse
	lookupSubjectError     error
	lookupSubjectRequests  []*v1.LookupSubjectsRequest
	readResponseBatches    [][]*v1.ReadRelationshipsResponse
	readError              error
	readRequests           []*v1.ReadRelationshipsRequest
	writeToken             string
	writeError             error
	writeRequests          []*v1.WriteRelationshipsRequest
}

func (fake *typedPermissionsService) CheckPermission(
	_ context.Context,
	request *v1.CheckPermissionRequest,
	_ ...grpc.CallOption,
) (*v1.CheckPermissionResponse, error) {
	fake.checkRequests = append(fake.checkRequests, proto.Clone(request).(*v1.CheckPermissionRequest))
	return fake.checkResponse, fake.checkError
}

func (fake *typedPermissionsService) WriteRelationships(
	_ context.Context,
	request *v1.WriteRelationshipsRequest,
	_ ...grpc.CallOption,
) (*v1.WriteRelationshipsResponse, error) {
	fake.writeRequests = append(fake.writeRequests, proto.Clone(request).(*v1.WriteRelationshipsRequest))
	if fake.writeError != nil {
		return nil, fake.writeError
	}
	return &v1.WriteRelationshipsResponse{WrittenAt: &v1.ZedToken{Token: fake.writeToken}}, nil
}

func (fake *typedPermissionsService) LookupResources(
	_ context.Context,
	request *v1.LookupResourcesRequest,
	_ ...grpc.CallOption,
) (grpc.ServerStreamingClient[v1.LookupResourcesResponse], error) {
	fake.lookupRequests = append(fake.lookupRequests, proto.Clone(request).(*v1.LookupResourcesRequest))
	if fake.lookupError != nil {
		return nil, fake.lookupError
	}
	return &typedLookupResourcesStream{responses: fake.lookupResponses}, nil
}

func (fake *typedPermissionsService) LookupSubjects(
	_ context.Context,
	request *v1.LookupSubjectsRequest,
	_ ...grpc.CallOption,
) (grpc.ServerStreamingClient[v1.LookupSubjectsResponse], error) {
	fake.lookupSubjectRequests = append(fake.lookupSubjectRequests, proto.Clone(request).(*v1.LookupSubjectsRequest))
	if fake.lookupSubjectError != nil {
		return nil, fake.lookupSubjectError
	}
	return &typedLookupSubjectsStream{responses: fake.lookupSubjectResponses}, nil
}

func (fake *typedPermissionsService) ReadRelationships(
	_ context.Context,
	request *v1.ReadRelationshipsRequest,
	_ ...grpc.CallOption,
) (grpc.ServerStreamingClient[v1.ReadRelationshipsResponse], error) {
	fake.readRequests = append(fake.readRequests, proto.Clone(request).(*v1.ReadRelationshipsRequest))
	if fake.readError != nil {
		return nil, fake.readError
	}
	var responses []*v1.ReadRelationshipsResponse
	if len(fake.readResponseBatches) != 0 {
		responses = fake.readResponseBatches[0]
		fake.readResponseBatches = fake.readResponseBatches[1:]
	}
	return &typedReadRelationshipsStream{responses: responses}, nil
}

type typedLookupResourcesStream struct {
	grpc.ClientStream
	responses []*v1.LookupResourcesResponse
}

func (stream *typedLookupResourcesStream) Recv() (*v1.LookupResourcesResponse, error) {
	if len(stream.responses) == 0 {
		return nil, io.EOF
	}
	response := stream.responses[0]
	stream.responses = stream.responses[1:]
	return response, nil
}

type typedLookupSubjectsStream struct {
	grpc.ClientStream
	responses []*v1.LookupSubjectsResponse
}

type typedReadRelationshipsStream struct {
	grpc.ClientStream
	responses []*v1.ReadRelationshipsResponse
}

func (stream *typedReadRelationshipsStream) Recv() (*v1.ReadRelationshipsResponse, error) {
	if len(stream.responses) == 0 {
		return nil, io.EOF
	}
	response := stream.responses[0]
	stream.responses = stream.responses[1:]
	return response, nil
}

func (stream *typedLookupSubjectsStream) Recv() (*v1.LookupSubjectsResponse, error) {
	if len(stream.responses) == 0 {
		return nil, io.EOF
	}
	response := stream.responses[0]
	stream.responses = stream.responses[1:]
	return response, nil
}
