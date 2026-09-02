package auth

import (
	"context"
	"fmt"
	"io"
	"sort"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const snapshotProbeAccountIdentityID = "00000000-0000-0000-0000-000000000000"

// RelationshipSnapshot is one immutable relationship observed from SpiceDB.
// It is intentionally read-only: the owning domain must exhaustively validate
// these values and reconstruct its exact generated mutation descriptors.
type RelationshipSnapshot struct {
	resourceType    string
	resourceID      string
	relation        string
	subjectType     string
	subjectID       string
	subjectRelation string
}

func (snapshot RelationshipSnapshot) ResourceType() string    { return snapshot.resourceType }
func (snapshot RelationshipSnapshot) ResourceID() string      { return snapshot.resourceID }
func (snapshot RelationshipSnapshot) Relation() string        { return snapshot.relation }
func (snapshot RelationshipSnapshot) SubjectType() string     { return snapshot.subjectType }
func (snapshot RelationshipSnapshot) SubjectID() string       { return snapshot.subjectID }
func (snapshot RelationshipSnapshot) SubjectRelation() string { return snapshot.subjectRelation }

// Matches reports whether the observed tuple has the exact key fixed by one
// generated typed mutation. The mutation operation is intentionally ignored:
// touch and delete describe the same relationship key.
func (snapshot RelationshipSnapshot) Matches(mutation policyv1.RelationshipMutation) bool {
	return mutation.Valid() && mutation.Resource().Valid() &&
		snapshot.resourceType == mutation.Resource().Type() &&
		snapshot.resourceID == mutation.Resource().ID() &&
		snapshot.relation == mutation.Relation() &&
		snapshot.subjectType == mutation.SubjectType() &&
		snapshot.subjectID == mutation.SubjectID() &&
		snapshot.subjectRelation == mutation.SubjectRelation()
}

// RelationshipMutationPair binds the generated delete and touch descriptors
// for one exact relationship key while a domain translates a read-only
// snapshot into a reversible deletion batch.
type RelationshipMutationPair struct {
	delete  policyv1.RelationshipMutation
	restore policyv1.RelationshipMutation
}

func NewRelationshipMutationPair(deleteMutation, restoreMutation policyv1.RelationshipMutation) (RelationshipMutationPair, error) {
	if !deleteMutation.Valid() || deleteMutation.Operation() != policyv1.RelationshipDelete {
		return RelationshipMutationPair{}, fmt.Errorf("relationship delete descriptor is invalid")
	}
	if !restoreMutation.Valid() || restoreMutation.Operation() != policyv1.RelationshipTouch {
		return RelationshipMutationPair{}, fmt.Errorf("relationship restore descriptor is invalid")
	}
	if deleteMutation.Resource().Type() != restoreMutation.Resource().Type() ||
		deleteMutation.Resource().ID() != restoreMutation.Resource().ID() ||
		deleteMutation.Relation() != restoreMutation.Relation() ||
		deleteMutation.SubjectType() != restoreMutation.SubjectType() ||
		deleteMutation.SubjectID() != restoreMutation.SubjectID() ||
		deleteMutation.SubjectRelation() != restoreMutation.SubjectRelation() {
		return RelationshipMutationPair{}, fmt.Errorf("relationship delete and restore descriptors target different keys")
	}
	return RelationshipMutationPair{delete: deleteMutation, restore: restoreMutation}, nil
}

func (pair RelationshipMutationPair) Delete() policyv1.RelationshipMutation  { return pair.delete }
func (pair RelationshipMutationPair) Restore() policyv1.RelationshipMutation { return pair.restore }
func (pair RelationshipMutationPair) Matches(snapshot RelationshipSnapshot) bool {
	return snapshot.Matches(pair.delete)
}

// SnapshotResourceRelationshipDescriptors reads exact SpiceDB state for one
// generated resource descriptor. Unknown observed tuples are returned, not
// translated; the owning domain must reject anything outside its generated
// relationship constructors before issuing a write.
func (c *SpiceDBClient) SnapshotResourceRelationshipDescriptors(
	ctx context.Context,
	plan policyv1.RelationshipSnapshotPlan,
) ([]RelationshipSnapshot, ZedToken, error) {
	if !plan.Valid() {
		return nil, ZedToken{}, fmt.Errorf("resource relationship snapshot plan is invalid")
	}
	resource := plan.Resource()
	if err := validateTypedEngineComponent("resource snapshot type", resource.Type()); err != nil {
		return nil, ZedToken{}, err
	}
	if err := validateTypedEngineComponent("resource snapshot id", resource.ID()); err != nil {
		return nil, ZedToken{}, err
	}
	relationships, readAt, err := c.readResourceRelationships(ctx, plan)
	if err != nil {
		return nil, ZedToken{}, err
	}
	snapshots := make([]RelationshipSnapshot, 0, len(relationships))
	for _, relationship := range relationships {
		if relationship == nil || relationship.GetResource() == nil || relationship.GetSubject() == nil || relationship.GetSubject().GetObject() == nil {
			return nil, ZedToken{}, fmt.Errorf("SpiceDB returned an invalid snapshot relationship")
		}
		snapshots = append(snapshots, RelationshipSnapshot{
			resourceType:    relationship.GetResource().GetObjectType(),
			resourceID:      relationship.GetResource().GetObjectId(),
			relation:        relationship.GetRelation(),
			subjectType:     relationship.GetSubject().GetObject().GetObjectType(),
			subjectID:       relationship.GetSubject().GetObject().GetObjectId(),
			subjectRelation: relationship.GetSubject().GetOptionalRelation(),
		})
	}
	return snapshots, readAt, nil
}

func (c *SpiceDBClient) readResourceRelationships(
	ctx context.Context,
	plan policyv1.RelationshipSnapshotPlan,
) ([]*v1.Relationship, ZedToken, error) {
	if c == nil || c.client == nil {
		return nil, ZedToken{}, fmt.Errorf("SpiceDB client is not configured")
	}
	if !plan.Valid() {
		return nil, ZedToken{}, fmt.Errorf("resource relationship snapshot plan is invalid")
	}
	resourceDescriptor := plan.Resource()
	resource := &v1.ObjectReference{ObjectType: resourceDescriptor.Type(), ObjectId: resourceDescriptor.ID()}
	probe, err := c.client.CheckPermission(ctx, &v1.CheckPermissionRequest{
		Resource:    resource,
		Permission:  plan.ProbePermission(),
		Subject:     &v1.SubjectReference{Object: &v1.ObjectReference{ObjectType: spiceDBAccountIdentityObjectType, ObjectId: snapshotProbeAccountIdentityID}},
		Consistency: fullyConsistentSpiceDB(),
	})
	if err != nil {
		return nil, ZedToken{}, fmt.Errorf("establish resource relationship snapshot: %w", err)
	}
	if probe == nil {
		return nil, ZedToken{}, fmt.Errorf("SpiceDB returned an empty snapshot probe response")
	}
	readAt, err := zedTokenFromResponse(probe.GetCheckedAt(), "resource relationship snapshot")
	if err != nil {
		return nil, ZedToken{}, err
	}

	relationships := make(map[snapshotRelationshipKey]*v1.Relationship)
	outgoing := &v1.RelationshipFilter{ResourceType: resource.ObjectType, OptionalResourceId: resource.ObjectId}
	if err := c.collectSnapshotRelationships(ctx, outgoing, readAt, relationships); err != nil {
		return nil, ZedToken{}, fmt.Errorf("read outgoing resource relationships: %w", err)
	}
	if parentRelation := plan.IncomingParentRelation(); parentRelation != "" {
		incomingParent := &v1.RelationshipFilter{
			ResourceType:     resourceDescriptor.Type(),
			OptionalRelation: parentRelation,
			OptionalSubjectFilter: &v1.SubjectFilter{
				SubjectType:       resourceDescriptor.Type(),
				OptionalSubjectId: resourceDescriptor.ID(),
			},
		}
		if err := c.collectSnapshotRelationships(ctx, incomingParent, readAt, relationships); err != nil {
			return nil, ZedToken{}, fmt.Errorf("read incoming parent relationships: %w", err)
		}
	}

	keys := make([]snapshotRelationshipKey, 0, len(relationships))
	for key := range relationships {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].less(keys[j]) })
	ordered := make([]*v1.Relationship, 0, len(keys))
	for _, key := range keys {
		ordered = append(ordered, relationships[key])
	}
	return ordered, readAt, nil
}

// VerifyResourceRelationshipsDeleted proves that a generated resource has no
// relationships at or after the supplied mutation revision.
func (c *SpiceDBClient) VerifyResourceRelationshipsDeleted(
	ctx context.Context,
	plan policyv1.RelationshipSnapshotPlan,
	token ZedToken,
) error {
	if !plan.Valid() {
		return fmt.Errorf("resource deletion verification snapshot plan is invalid")
	}
	resource := plan.Resource()
	if err := validateTypedEngineComponent("resource deletion verification type", resource.Type()); err != nil {
		return err
	}
	if err := validateTypedEngineComponent("resource deletion verification id", resource.ID()); err != nil {
		return err
	}
	return c.verifyResourceRelationshipsDeletedAtLeastAsFresh(ctx, plan, token)
}

// VerifyResourceRelationshipsDeletedAtLeastAsFresh proves that a resource has
// no outgoing relationships and no supported incoming parent edges at or after
// the supplied mutation revision.
func (c *SpiceDBClient) verifyResourceRelationshipsDeletedAtLeastAsFresh(ctx context.Context, plan policyv1.RelationshipSnapshotPlan, token ZedToken) error {
	if c == nil || c.client == nil {
		return fmt.Errorf("SpiceDB client is not configured")
	}
	if !plan.Valid() {
		return fmt.Errorf("resource deletion verification snapshot plan is invalid")
	}
	resourceDescriptor := plan.Resource()
	resource := &v1.ObjectReference{ObjectType: resourceDescriptor.Type(), ObjectId: resourceDescriptor.ID()}
	filters := []*v1.RelationshipFilter{{ResourceType: resource.ObjectType, OptionalResourceId: resource.ObjectId}}
	if parentRelation := plan.IncomingParentRelation(); parentRelation != "" {
		filters = append(filters, &v1.RelationshipFilter{
			ResourceType:     resourceDescriptor.Type(),
			OptionalRelation: parentRelation,
			OptionalSubjectFilter: &v1.SubjectFilter{
				SubjectType:       resourceDescriptor.Type(),
				OptionalSubjectId: resourceDescriptor.ID(),
			},
		})
	}
	for _, filter := range filters {
		consistency, consistencyErr := atLeastAsFreshSpiceDB(token)
		if consistencyErr != nil {
			return consistencyErr
		}
		stream, readErr := c.client.ReadRelationships(ctx, &v1.ReadRelationshipsRequest{RelationshipFilter: filter, Consistency: consistency})
		if readErr != nil {
			return fmt.Errorf("verify deleted resource relationships: %w", readErr)
		}
		response, recvErr := stream.Recv()
		if recvErr == io.EOF {
			continue
		}
		if recvErr != nil {
			return fmt.Errorf("verify deleted resource relationships: %w", recvErr)
		}
		if response != nil && response.GetRelationship() != nil {
			return fmt.Errorf("resource %s:%s still has a SpiceDB relationship", resourceDescriptor.Type(), resourceDescriptor.ID())
		}
		return fmt.Errorf("SpiceDB returned an empty relationship response")
	}
	return nil
}

func (c *SpiceDBClient) collectSnapshotRelationships(ctx context.Context, filter *v1.RelationshipFilter, readAt ZedToken, relationships map[snapshotRelationshipKey]*v1.Relationship) error {
	consistency, err := atExactSnapshotSpiceDB(readAt)
	if err != nil {
		return err
	}
	stream, err := c.client.ReadRelationships(ctx, &v1.ReadRelationshipsRequest{RelationshipFilter: filter, Consistency: consistency})
	if err != nil {
		return err
	}
	for {
		response, recvErr := stream.Recv()
		if recvErr == io.EOF {
			return nil
		}
		if recvErr != nil {
			return recvErr
		}
		if response == nil || response.GetRelationship() == nil {
			return fmt.Errorf("SpiceDB returned an empty relationship response")
		}
		relationship := response.GetRelationship()
		key, keyErr := newSnapshotRelationshipKey(relationship)
		if keyErr != nil {
			return keyErr
		}
		relationships[key] = relationship
	}
}

type snapshotRelationshipKey struct {
	resourceType    string
	resourceID      string
	relation        string
	subjectType     string
	subjectID       string
	subjectRelation string
}

func (key snapshotRelationshipKey) less(other snapshotRelationshipKey) bool {
	left := [...]string{key.resourceType, key.resourceID, key.relation, key.subjectType, key.subjectID, key.subjectRelation}
	right := [...]string{other.resourceType, other.resourceID, other.relation, other.subjectType, other.subjectID, other.subjectRelation}
	for index := range left {
		if left[index] != right[index] {
			return left[index] < right[index]
		}
	}
	return false
}

func newSnapshotRelationshipKey(relationship *v1.Relationship) (snapshotRelationshipKey, error) {
	if relationship == nil || relationship.GetResource() == nil || relationship.GetSubject() == nil || relationship.GetSubject().GetObject() == nil {
		return snapshotRelationshipKey{}, fmt.Errorf("invalid SpiceDB snapshot relationship")
	}
	return snapshotRelationshipKey{
		resourceType:    relationship.GetResource().GetObjectType(),
		resourceID:      relationship.GetResource().GetObjectId(),
		relation:        relationship.GetRelation(),
		subjectType:     relationship.GetSubject().GetObject().GetObjectType(),
		subjectID:       relationship.GetSubject().GetObject().GetObjectId(),
		subjectRelation: relationship.GetSubject().GetOptionalRelation(),
	}, nil
}
