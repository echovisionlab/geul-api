package auth

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Can is the single typed policy-enforcement entrypoint. Domain code supplies
// the complete generated decision; this adapter uses only its actor, resource,
// and engine permission for one fully-consistent SpiceDB check. Delegation is
// retained by the decision for attribution and cannot alter the engine key.
func (c *SpiceDBClient) Can(
	ctx context.Context,
	decision policyv1.AuthorizationDecision,
) (bool, error) {
	request, err := authorizationDecisionCheckRequest(decision)
	if err != nil {
		return false, err
	}
	if c == nil || c.client == nil {
		return false, fmt.Errorf("SpiceDB client is not configured")
	}
	return c.executePermissionCheck(ctx, request)
}

func authorizationDecisionCheckRequest(decision policyv1.AuthorizationDecision) (*v1.CheckPermissionRequest, error) {
	if !decision.Valid() || !decision.Actor().Valid() || !decision.Delegation().Valid() || !decision.Resource().Valid() || !decision.Action().Valid() {
		return nil, fmt.Errorf("authorization decision is invalid")
	}
	request, err := typedActorCanCheckRequest(
		decision.Actor(),
		decision.Resource(),
		decision.Action(),
		decision.EngineKey(),
	)
	if err != nil {
		return nil, fmt.Errorf("authorization decision: %w", err)
	}
	return request, nil
}

// CheckActorCan checks one explicit account identity's generated Can
// descriptor for an internal/system workflow or effective-authority
// calculation. It carries no request delegation or audit attribution and must
// never replace AuthorizationDecision plus Can for external request admission.
func (c *SpiceDBClient) CheckActorCan(
	ctx context.Context,
	actor policyv1.Actor,
	can policyv1.Can,
) (bool, error) {
	if !can.Valid() {
		return false, fmt.Errorf("actor capability Can descriptor is invalid")
	}
	request, err := typedActorCanCheckRequest(actor, can.Resource(), can.Action(), can.EngineKey())
	if err != nil {
		return false, fmt.Errorf("actor capability check: %w", err)
	}
	if c == nil || c.client == nil {
		return false, fmt.Errorf("SpiceDB client is not configured")
	}
	return c.executePermissionCheck(ctx, request)
}

func typedActorCanCheckRequest(
	actor policyv1.Actor,
	resource policyv1.Resource,
	action policyv1.Action,
	engineKey string,
) (*v1.CheckPermissionRequest, error) {
	if !actor.Valid() || !resource.Valid() || !action.Valid() {
		return nil, fmt.Errorf("actor, resource, and action descriptors are required")
	}
	if err := validateTypedEngineComponent("resource type", resource.Type()); err != nil {
		return nil, err
	}
	if err := validateTypedEngineComponent("resource id", resource.ID()); err != nil {
		return nil, err
	}
	permission := action.Permission()
	if err := validateTypedEngineComponent("permission", permission); err != nil {
		return nil, err
	}
	subject, err := NewAccountIdentitySubject(IdentityID(actor.AccountIdentityID()))
	if err != nil {
		return nil, fmt.Errorf("actor: %w", err)
	}

	expectedEngineKey := strings.Join([]string{
		resource.Type(),
		resource.ID(),
		permission,
	}, "\x00")
	if engineKey != expectedEngineKey {
		return nil, fmt.Errorf("engine key is inconsistent")
	}

	return &v1.CheckPermissionRequest{
		Resource: &v1.ObjectReference{
			ObjectType: resource.Type(),
			ObjectId:   resource.ID(),
		},
		Permission: permission,
		Subject: &v1.SubjectReference{Object: &v1.ObjectReference{
			ObjectType: spiceDBAccountIdentityObjectType,
			ObjectId:   subject.ID.String(),
		}},
		Consistency: fullyConsistentSpiceDB(),
	}, nil
}

func validateTypedEngineComponent(kind, value string) error {
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%s is invalid", kind)
	}
	return nil
}

func (c *SpiceDBClient) executePermissionCheck(ctx context.Context, request *v1.CheckPermissionRequest) (bool, error) {
	startedAt := time.Now()
	response, err := c.client.CheckPermission(ctx, request)
	if err != nil {
		defaultSpiceDBMetrics.recordCheck(ctx, startedAt, spiceDBOutcomeFailed)
		return false, err
	}
	if response == nil {
		defaultSpiceDBMetrics.recordCheck(ctx, startedAt, spiceDBOutcomeFailed)
		return false, fmt.Errorf("SpiceDB returned an empty permission response")
	}
	allowed := response.Permissionship == v1.CheckPermissionResponse_PERMISSIONSHIP_HAS_PERMISSION
	if allowed {
		defaultSpiceDBMetrics.recordCheck(ctx, startedAt, spiceDBOutcomeAllowed)
	} else {
		defaultSpiceDBMetrics.recordCheck(ctx, startedAt, spiceDBOutcomeDenied)
	}
	return allowed, nil
}

// ReadDirectGlobalRole reads the exact direct role tuple for one account
// identity. It does not use inherited platform permissions: an absent direct
// tuple is reported as the logical user default so account bootstrap can write
// the explicit singleton relation when required.
func (c *SpiceDBClient) ReadDirectGlobalRole(
	ctx context.Context,
	subject AccountIdentitySubject,
) (role policyv1.RoleID, found bool, err error) {
	return c.readDirectGlobalRole(ctx, subject, fullyConsistentSpiceDB())
}

func (c *SpiceDBClient) ReadDirectGlobalRoleAtLeastAsFresh(
	ctx context.Context,
	subject AccountIdentitySubject,
	token ZedToken,
) (role policyv1.RoleID, found bool, err error) {
	consistency, err := atLeastAsFreshSpiceDB(token)
	if err != nil {
		return policyv1.RoleID{}, false, err
	}
	return c.readDirectGlobalRole(ctx, subject, consistency)
}

func (c *SpiceDBClient) readDirectGlobalRole(
	ctx context.Context,
	subject AccountIdentitySubject,
	consistency *v1.Consistency,
) (role policyv1.RoleID, found bool, err error) {
	if c == nil || c.client == nil {
		return policyv1.RoleID{}, false, fmt.Errorf("SpiceDB client is not configured")
	}
	if _, err := NewAccountIdentitySubject(subject.ID); err != nil {
		return policyv1.RoleID{}, false, err
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return policyv1.RoleID{}, false, err
	}
	membership, err := policyv1.Role.TouchMember(policyv1.Role.User(), actor)
	if err != nil {
		return policyv1.RoleID{}, false, err
	}
	stream, err := c.client.ReadRelationships(ctx, &v1.ReadRelationshipsRequest{
		RelationshipFilter: &v1.RelationshipFilter{
			ResourceType:          membership.Resource().Type(),
			OptionalRelation:      membership.Relation(),
			OptionalSubjectFilter: &v1.SubjectFilter{SubjectType: membership.SubjectType(), OptionalSubjectId: subject.ID.String()},
		},
		Consistency: consistency,
	})
	if err != nil {
		return policyv1.RoleID{}, false, fmt.Errorf("read direct global role: %w", err)
	}
	var roles []policyv1.RoleID
	for {
		response, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return policyv1.RoleID{}, false, fmt.Errorf("read direct global role: %w", recvErr)
		}
		if response == nil || response.GetRelationship() == nil || response.GetRelationship().GetResource() == nil || response.GetRelationship().GetSubject() == nil || response.GetRelationship().GetSubject().GetObject() == nil {
			return policyv1.RoleID{}, false, fmt.Errorf("read direct global role: empty SpiceDB relationship response")
		}
		relationship := response.GetRelationship()
		role, validRole := policyv1.Role.Parse(relationship.GetResource().GetObjectId())
		if !validRole || relationship.GetResource().GetObjectType() != membership.Resource().Type() || relationship.GetRelation() != membership.Relation() || relationship.GetSubject().GetObject().GetObjectType() != membership.SubjectType() || relationship.GetSubject().GetObject().GetObjectId() != subject.ID.String() || relationship.GetSubject().GetOptionalRelation() != "" {
			return policyv1.RoleID{}, false, fmt.Errorf("read direct global role: invalid SpiceDB relationship")
		}
		roles = append(roles, role)
	}
	if len(roles) == 0 {
		return policyv1.Role.User(), false, nil
	}
	if len(roles) != 1 {
		return policyv1.RoleID{}, false, fmt.Errorf("read direct global role: account identity has %d direct roles", len(roles))
	}
	return roles[0], true, nil
}

// LookupGlobalSubjects returns the account identities selected by one
// generated platform subject-enumeration descriptor. Callers must hydrate
// product state separately.
func (c *SpiceDBClient) LookupGlobalSubjects(
	ctx context.Context,
	lookup policyv1.SubjectLookup,
) ([]AccountIdentitySubject, error) {
	request, err := globalSubjectLookupRequest(lookup)
	if err != nil {
		return nil, err
	}
	if c == nil || c.client == nil {
		return nil, fmt.Errorf("SpiceDB client is not configured")
	}
	stream, err := c.client.LookupSubjects(ctx, request)
	if err != nil {
		return nil, err
	}
	var subjects []AccountIdentitySubject
	for {
		response, recvErr := stream.Recv()
		if recvErr == io.EOF {
			return subjects, nil
		}
		if recvErr != nil {
			return nil, recvErr
		}
		if response == nil || response.Subject == nil {
			return nil, fmt.Errorf("SpiceDB returned an empty global subject")
		}
		subject, subjectErr := NewAccountIdentitySubject(IdentityID(response.Subject.SubjectObjectId))
		if subjectErr != nil {
			return nil, fmt.Errorf("invalid global account identity subject: %w", subjectErr)
		}
		subjects = append(subjects, subject)
	}
}

func globalSubjectLookupRequest(lookup policyv1.SubjectLookup) (*v1.LookupSubjectsRequest, error) {
	if !lookup.Valid() {
		return nil, fmt.Errorf("global subject lookup descriptor is invalid")
	}
	for _, component := range []struct {
		kind  string
		value string
	}{
		{kind: "subject lookup resource type", value: lookup.ResourceType()},
		{kind: "subject lookup resource id", value: lookup.ResourceID()},
		{kind: "subject lookup permission", value: lookup.Permission()},
		{kind: "subject lookup subject type", value: lookup.SubjectType()},
	} {
		if err := validateTypedEngineComponent(component.kind, component.value); err != nil {
			return nil, err
		}
	}
	if lookup.SubjectType() != spiceDBAccountIdentityObjectType {
		return nil, fmt.Errorf("global subject lookup must select account identities")
	}
	return &v1.LookupSubjectsRequest{
		Resource: &v1.ObjectReference{
			ObjectType: lookup.ResourceType(),
			ObjectId:   lookup.ResourceID(),
		},
		Permission:        lookup.Permission(),
		SubjectObjectType: lookup.SubjectType(),
		Consistency:       fullyConsistentSpiceDB(),
	}, nil
}

// LookupResources returns resource IDs selected by one generated domain-owned
// lookup descriptor for one account-identity actor. SpiceDB remains the
// listing authority; PostgreSQL is only used by callers to hydrate returned
// product rows.
func (c *SpiceDBClient) LookupResources(
	ctx context.Context,
	lookup policyv1.ResourceLookup,
	actor policyv1.Actor,
) ([]string, error) {
	request, err := resourceLookupRequest(lookup, actor)
	if err != nil {
		return nil, err
	}
	if c == nil || c.client == nil {
		return nil, fmt.Errorf("SpiceDB client is not configured")
	}
	stream, err := c.client.LookupResources(ctx, request)
	if err != nil {
		return nil, err
	}
	var resourceIDs []string
	for {
		item, recvErr := stream.Recv()
		if recvErr == io.EOF {
			return resourceIDs, nil
		}
		if recvErr != nil {
			return nil, recvErr
		}
		if item == nil || item.GetResourceObjectId() == "" {
			return nil, fmt.Errorf("SpiceDB returned an empty resource")
		}
		resourceIDs = append(resourceIDs, item.GetResourceObjectId())
	}
}

func resourceLookupRequest(
	lookup policyv1.ResourceLookup,
	actor policyv1.Actor,
) (*v1.LookupResourcesRequest, error) {
	if !lookup.Valid() {
		return nil, fmt.Errorf("resource lookup descriptor is invalid")
	}
	if err := validateTypedEngineComponent("resource type", lookup.ResourceType()); err != nil {
		return nil, fmt.Errorf("resource lookup descriptor: %w", err)
	}
	if err := validateTypedEngineComponent("permission", lookup.Permission()); err != nil {
		return nil, fmt.Errorf("resource lookup descriptor: %w", err)
	}
	if !actor.Valid() {
		return nil, fmt.Errorf("resource lookup actor is invalid")
	}
	subject, err := NewAccountIdentitySubject(IdentityID(actor.AccountIdentityID()))
	if err != nil {
		return nil, fmt.Errorf("resource lookup actor: %w", err)
	}

	return &v1.LookupResourcesRequest{
		ResourceObjectType: lookup.ResourceType(),
		Permission:         lookup.Permission(),
		Subject: &v1.SubjectReference{Object: &v1.ObjectReference{
			ObjectType: spiceDBAccountIdentityObjectType,
			ObjectId:   subject.ID.String(),
		}},
		Consistency: fullyConsistentSpiceDB(),
	}, nil
}
