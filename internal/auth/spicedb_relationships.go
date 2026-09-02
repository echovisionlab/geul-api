package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const relationshipWriteMaxAttempts = 3

var relationshipWriteRetryDelays = [...]time.Duration{10 * time.Millisecond, 50 * time.Millisecond}

type relationshipMutation struct {
	update *v1.RelationshipUpdate
}

// ApplyRelationships is the typed relationship-write entrypoint. Every
// descriptor must come from a generated domain constructor; the complete batch
// is translated and validated before the sole SpiceDB write is attempted.
func (c *SpiceDBClient) ApplyRelationships(
	ctx context.Context,
	mutations ...policyv1.RelationshipMutation,
) (ZedToken, error) {
	translated, err := translateRelationshipMutations(mutations)
	if err != nil {
		return ZedToken{}, err
	}
	return c.writeRelationships(ctx, translated...)
}

// ApplyRelationshipsExpecting atomically asserts the exact pre-state described
// by compensate, then applies a generated relationship batch. Both complete
// descriptor batches are validated before SpiceDB I/O.
func (c *SpiceDBClient) ApplyRelationshipsExpecting(
	ctx context.Context,
	apply []policyv1.RelationshipMutation,
	compensate []policyv1.RelationshipMutation,
) (ZedToken, error) {
	translatedApply, err := translateRelationshipMutations(apply)
	if err != nil {
		return ZedToken{}, fmt.Errorf("apply relationships: %w", err)
	}
	translatedCompensate, err := translateRelationshipMutations(compensate)
	if err != nil {
		return ZedToken{}, fmt.Errorf("compensate relationships: %w", err)
	}
	return c.writeRelationshipsExpecting(ctx, translatedApply, translatedCompensate)
}

// CompensateRelationshipsExpecting restores a generated relationship batch
// only when the exact forward state still holds. Both complete descriptor
// batches are validated before SpiceDB I/O.
func (c *SpiceDBClient) CompensateRelationshipsExpecting(
	ctx context.Context,
	restore []policyv1.RelationshipMutation,
	expectedCurrent []policyv1.RelationshipMutation,
) (ZedToken, error) {
	translatedRestore, err := translateRelationshipMutations(restore)
	if err != nil {
		return ZedToken{}, fmt.Errorf("restore relationships: %w", err)
	}
	translatedExpectedCurrent, err := translateRelationshipMutations(expectedCurrent)
	if err != nil {
		return ZedToken{}, fmt.Errorf("expected current relationships: %w", err)
	}
	return c.restoreRelationshipsExpecting(ctx, translatedRestore, translatedExpectedCurrent)
}

func translateRelationshipMutations(mutations []policyv1.RelationshipMutation) ([]relationshipMutation, error) {
	if len(mutations) == 0 {
		return nil, fmt.Errorf("at least one relationship mutation is required")
	}
	translated := make([]relationshipMutation, len(mutations))
	for index, mutation := range mutations {
		converted, err := translateRelationshipMutation(mutation)
		if err != nil {
			return nil, fmt.Errorf("relationship mutation %d: %w", index, err)
		}
		translated[index] = converted
	}
	return translated, nil
}

func translateRelationshipMutation(mutation policyv1.RelationshipMutation) (relationshipMutation, error) {
	if !mutation.Valid() || !mutation.Resource().Valid() {
		return relationshipMutation{}, fmt.Errorf("descriptor is invalid")
	}
	resource := mutation.Resource()
	if err := validateTypedEngineComponent("relationship resource type", resource.Type()); err != nil {
		return relationshipMutation{}, err
	}
	if err := validateTypedEngineComponent("relationship resource id", resource.ID()); err != nil {
		return relationshipMutation{}, err
	}
	if err := validateTypedEngineComponent("relationship relation", mutation.Relation()); err != nil {
		return relationshipMutation{}, err
	}
	subject, err := typedRelationshipSubject(mutation)
	if err != nil {
		return relationshipMutation{}, err
	}

	var operation v1.RelationshipUpdate_Operation
	switch mutation.Operation() {
	case policyv1.RelationshipTouch:
		operation = v1.RelationshipUpdate_OPERATION_TOUCH
	case policyv1.RelationshipDelete:
		operation = v1.RelationshipUpdate_OPERATION_DELETE
	default:
		return relationshipMutation{}, fmt.Errorf("operation is invalid")
	}
	return relationshipMutation{update: &v1.RelationshipUpdate{
		Operation: operation,
		Relationship: &v1.Relationship{
			Resource: &v1.ObjectReference{ObjectType: resource.Type(), ObjectId: resource.ID()},
			Relation: mutation.Relation(),
			Subject:  subject,
		},
	}}, nil
}

func typedRelationshipSubject(mutation policyv1.RelationshipMutation) (*v1.SubjectReference, error) {
	// RelationshipMutation deliberately exposes only validated subject fields;
	// its private representation cannot be forged outside event-contracts.
	subjectType := mutation.SubjectType()
	subjectID := mutation.SubjectID()
	if err := validateTypedEngineComponent("relationship subject type", subjectType); err != nil {
		return nil, err
	}
	if err := validateTypedEngineComponent("relationship subject id", subjectID); err != nil {
		return nil, err
	}
	if mutation.SubjectRelation() != "" {
		if err := validateTypedEngineComponent("relationship subject relation", mutation.SubjectRelation()); err != nil {
			return nil, err
		}
	}
	if subjectType == spiceDBAccountIdentityObjectType {
		if _, err := NewAccountIdentitySubject(IdentityID(subjectID)); err != nil {
			return nil, fmt.Errorf("subject: %w", err)
		}
	}
	return &v1.SubjectReference{
		Object:           &v1.ObjectReference{ObjectType: subjectType, ObjectId: subjectID},
		OptionalRelation: mutation.SubjectRelation(),
	}, nil
}

func (c *SpiceDBClient) writeRelationships(ctx context.Context, mutations ...relationshipMutation) (ZedToken, error) {
	if c == nil || c.client == nil {
		return ZedToken{}, fmt.Errorf("SpiceDB client is not configured")
	}
	updates, err := relationshipUpdates(mutations)
	if err != nil {
		return ZedToken{}, err
	}
	startedAt := time.Now()
	token, err := c.writeIdempotentRelationships(ctx, &v1.WriteRelationshipsRequest{Updates: updates})
	if err != nil {
		defaultSpiceDBMetrics.recordWrite(ctx, startedAt, spiceDBWriteOperationBatch, spiceDBWriteOutcome(err))
		return ZedToken{}, err
	}
	defaultSpiceDBMetrics.recordWrite(ctx, startedAt, spiceDBWriteOperationBatch, spiceDBOutcomeSucceeded)
	return token, nil
}

// ConditionalRelationshipWrite atomically asserts the exact SpiceDB pre-state
// described by compensate, then applies the requested batch. Both batches must
// contain the same unique relationship keys; equal operations represent an
// asserted no-op whose state must still be preserved on rollback.
func (c *SpiceDBClient) writeRelationshipsExpecting(
	ctx context.Context,
	apply []relationshipMutation,
	compensate []relationshipMutation,
) (ZedToken, error) {
	if c == nil || c.client == nil {
		return ZedToken{}, fmt.Errorf("SpiceDB client is not configured")
	}
	updates, err := relationshipUpdates(apply)
	if err != nil {
		return ZedToken{}, err
	}
	preconditions, keys, err := expectedRelationshipPreconditions(apply, compensate)
	if err != nil {
		return ZedToken{}, err
	}
	startedAt := time.Now()
	token, err := c.writeExpectedRelationships(ctx, &v1.WriteRelationshipsRequest{
		Updates:               updates,
		OptionalPreconditions: preconditions,
	}, keys, expectedRelationshipWriteForward)
	if err != nil {
		defaultSpiceDBMetrics.recordWrite(ctx, startedAt, spiceDBWriteOperationBatch, spiceDBWriteOutcome(err))
		return ZedToken{}, err
	}
	defaultSpiceDBMetrics.recordWrite(ctx, startedAt, spiceDBWriteOperationBatch, spiceDBOutcomeSucceeded)
	return token, nil
}

// ConditionalRelationshipRestore conditionally restores an exact relationship
// pre-state after a failed database mutation. Unlike a forward write, it may
// accept an initial precondition failure only when a fully-consistent exact-key
// read proves that the restore state already holds.
func (c *SpiceDBClient) restoreRelationshipsExpecting(
	ctx context.Context,
	restore []relationshipMutation,
	expectedCurrent []relationshipMutation,
) (ZedToken, error) {
	if c == nil || c.client == nil {
		return ZedToken{}, fmt.Errorf("SpiceDB client is not configured")
	}
	updates, err := relationshipUpdates(restore)
	if err != nil {
		return ZedToken{}, err
	}
	preconditions, keys, err := expectedRelationshipPreconditions(restore, expectedCurrent)
	if err != nil {
		return ZedToken{}, err
	}
	startedAt := time.Now()
	token, err := c.writeExpectedRelationships(ctx, &v1.WriteRelationshipsRequest{
		Updates:               updates,
		OptionalPreconditions: preconditions,
	}, keys, expectedRelationshipWriteRestore)
	if err != nil {
		defaultSpiceDBMetrics.recordWrite(ctx, startedAt, spiceDBWriteOperationBatch, spiceDBWriteOutcome(err))
		return ZedToken{}, err
	}
	defaultSpiceDBMetrics.recordWrite(ctx, startedAt, spiceDBWriteOperationBatch, spiceDBOutcomeSucceeded)
	return token, nil
}

type relationshipWriteOutcomeUncertainError struct {
	err error
}

func (err *relationshipWriteOutcomeUncertainError) Error() string {
	return "SpiceDB relationship write outcome is uncertain: " + err.err.Error()
}

func (err *relationshipWriteOutcomeUncertainError) Unwrap() error { return err.err }

// IsRelationshipWriteOutcomeUncertain reports whether exact compensation must
// run because the provider did not establish a final write outcome.
func IsRelationshipWriteOutcomeUncertain(err error) bool {
	var target *relationshipWriteOutcomeUncertainError
	return errors.As(err, &target)
}

func relationshipUpdates(mutations []relationshipMutation) ([]*v1.RelationshipUpdate, error) {
	if len(mutations) == 0 {
		return nil, fmt.Errorf("at least one relationship mutation is required")
	}
	updates := make([]*v1.RelationshipUpdate, len(mutations))
	for index, mutation := range mutations {
		if mutation.update == nil || mutation.update.GetRelationship() == nil {
			return nil, fmt.Errorf("relationship mutation %d is invalid", index)
		}
		updates[index] = mutation.update
	}
	return updates, nil
}

type expectedRelationshipState struct {
	key            snapshotRelationshipKey
	desiredExists  bool
	preStateExists bool
}

func expectedRelationshipPreconditions(
	apply []relationshipMutation,
	compensate []relationshipMutation,
) ([]*v1.Precondition, []expectedRelationshipState, error) {
	if len(apply) == 0 || len(apply) != len(compensate) {
		return nil, nil, fmt.Errorf("authorization apply and compensation batches must contain the same non-zero relationship key set")
	}
	applyByKey := make(map[snapshotRelationshipKey]relationshipMutation, len(apply))
	for index, mutation := range apply {
		if mutation.update == nil || mutation.update.GetRelationship() == nil {
			return nil, nil, fmt.Errorf("relationship apply mutation %d is invalid", index)
		}
		key, err := newSnapshotRelationshipKey(mutation.update.GetRelationship())
		if err != nil {
			return nil, nil, fmt.Errorf("relationship apply mutation %d: %w", index, err)
		}
		if _, duplicate := applyByKey[key]; duplicate {
			return nil, nil, fmt.Errorf("relationship apply batch contains duplicate key at index %d", index)
		}
		applyByKey[key] = mutation
	}

	preconditions := make([]*v1.Precondition, 0, len(compensate))
	states := make([]expectedRelationshipState, 0, len(compensate))
	compensationKeys := make(map[snapshotRelationshipKey]struct{}, len(compensate))
	for index, inverse := range compensate {
		if inverse.update == nil || inverse.update.GetRelationship() == nil {
			return nil, nil, fmt.Errorf("relationship compensation mutation %d is invalid", index)
		}
		key, err := newSnapshotRelationshipKey(inverse.update.GetRelationship())
		if err != nil {
			return nil, nil, fmt.Errorf("relationship compensation mutation %d: %w", index, err)
		}
		if _, duplicate := compensationKeys[key]; duplicate {
			return nil, nil, fmt.Errorf("relationship compensation batch contains duplicate key at index %d", index)
		}
		compensationKeys[key] = struct{}{}
		forward, ok := applyByKey[key]
		if !ok {
			return nil, nil, fmt.Errorf("relationship compensation mutation %d has no matching apply key", index)
		}
		if !validRelationshipOperation(forward.update.GetOperation()) || !validRelationshipOperation(inverse.update.GetOperation()) {
			return nil, nil, fmt.Errorf("relationship mutation %d has an unsupported operation", index)
		}
		preStateExists := inverse.update.GetOperation() == v1.RelationshipUpdate_OPERATION_TOUCH
		operation := v1.Precondition_OPERATION_MUST_NOT_MATCH
		if preStateExists {
			operation = v1.Precondition_OPERATION_MUST_MATCH
		}
		precondition := &v1.Precondition{Operation: operation, Filter: exactRelationshipFilter(key)}
		preconditions = append(preconditions, precondition)
		states = append(states, expectedRelationshipState{
			key:            key,
			desiredExists:  forward.update.GetOperation() == v1.RelationshipUpdate_OPERATION_TOUCH,
			preStateExists: preStateExists,
		})
	}
	return preconditions, states, nil
}

func validRelationshipOperation(operation v1.RelationshipUpdate_Operation) bool {
	return operation == v1.RelationshipUpdate_OPERATION_TOUCH || operation == v1.RelationshipUpdate_OPERATION_DELETE
}

func exactRelationshipFilter(key snapshotRelationshipKey) *v1.RelationshipFilter {
	return &v1.RelationshipFilter{
		ResourceType:       key.resourceType,
		OptionalResourceId: key.resourceID,
		OptionalRelation:   key.relation,
		OptionalSubjectFilter: &v1.SubjectFilter{
			SubjectType:       key.subjectType,
			OptionalSubjectId: key.subjectID,
			OptionalRelation:  &v1.SubjectFilter_RelationFilter{Relation: key.subjectRelation},
		},
	}
}

func (c *SpiceDBClient) writeIdempotentRelationships(ctx context.Context, request *v1.WriteRelationshipsRequest) (ZedToken, error) {
	var lastErr error
	for attempt := 0; attempt < relationshipWriteMaxAttempts; attempt++ {
		token, err := c.writeRelationshipsOnce(ctx, request)
		if err == nil {
			return token, nil
		}
		lastErr = err
		if !ambiguousRelationshipWriteError(err) {
			return ZedToken{}, err
		}
		if waitErr := waitForRelationshipWriteRetry(ctx, attempt); waitErr != nil {
			return ZedToken{}, &relationshipWriteOutcomeUncertainError{err: errors.Join(lastErr, waitErr)}
		}
	}
	return ZedToken{}, &relationshipWriteOutcomeUncertainError{err: lastErr}
}

func (c *SpiceDBClient) writeExpectedRelationships(
	ctx context.Context,
	request *v1.WriteRelationshipsRequest,
	states []expectedRelationshipState,
	mode expectedRelationshipWriteMode,
) (ZedToken, error) {
	var (
		lastErr      error
		hadAmbiguity bool
	)
	for attempt := 0; attempt < relationshipWriteMaxAttempts; attempt++ {
		token, err := c.writeRelationshipsOnce(ctx, request)
		if err == nil {
			return token, nil
		}
		ambiguous := ambiguousRelationshipWriteError(err)
		inspectInitialRestoreFailure := mode == expectedRelationshipWriteRestore && status.Code(err) == codes.FailedPrecondition
		if !ambiguous && !hadAmbiguity && !inspectInitialRestoreFailure {
			return ZedToken{}, err
		}
		hadAmbiguity = hadAmbiguity || ambiguous
		lastErr = errors.Join(lastErr, err)

		state, readAt, inspectErr := c.inspectExpectedRelationshipStateBounded(ctx, states)
		if inspectErr == nil {
			switch state {
			case expectedRelationshipDesiredState:
				return readAt, nil
			case expectedRelationshipPreState:
				if !ambiguous {
					return ZedToken{}, err
				}
				if attempt == relationshipWriteMaxAttempts-1 {
					return ZedToken{}, &relationshipWriteOutcomeUncertainError{err: lastErr}
				}
			case expectedRelationshipConflictingState:
				conflictErr := fmt.Errorf("%w; exact relationship keys are in a mixed or conflicting state", lastErr)
				if hadAmbiguity {
					return ZedToken{}, &relationshipWriteOutcomeUncertainError{err: conflictErr}
				}
				return ZedToken{}, conflictErr
			}
		} else {
			lastErr = errors.Join(lastErr, fmt.Errorf("inspect exact relationship write state: %w", inspectErr))
			if !hadAmbiguity {
				return ZedToken{}, lastErr
			}
			return ZedToken{}, &relationshipWriteOutcomeUncertainError{err: lastErr}
		}
		if waitErr := waitForRelationshipWriteRetry(ctx, attempt); waitErr != nil {
			return ZedToken{}, &relationshipWriteOutcomeUncertainError{err: errors.Join(lastErr, waitErr)}
		}
	}
	return ZedToken{}, &relationshipWriteOutcomeUncertainError{err: lastErr}
}

type expectedRelationshipWriteMode int

const (
	expectedRelationshipWriteForward expectedRelationshipWriteMode = iota
	expectedRelationshipWriteRestore
)

func (c *SpiceDBClient) inspectExpectedRelationshipStateBounded(
	ctx context.Context,
	states []expectedRelationshipState,
) (expectedRelationshipInspection, ZedToken, error) {
	var lastErr error
	for attempt := 0; attempt < relationshipWriteMaxAttempts; attempt++ {
		state, readAt, err := c.inspectExpectedRelationshipState(ctx, states)
		if err == nil {
			return state, readAt, nil
		}
		lastErr = err
		if waitErr := waitForRelationshipWriteRetry(ctx, attempt); waitErr != nil {
			return expectedRelationshipConflictingState, ZedToken{}, errors.Join(lastErr, waitErr)
		}
	}
	return expectedRelationshipConflictingState, ZedToken{}, lastErr
}

func (c *SpiceDBClient) writeRelationshipsOnce(ctx context.Context, request *v1.WriteRelationshipsRequest) (ZedToken, error) {
	response, err := c.client.WriteRelationships(ctx, request)
	if err != nil {
		return ZedToken{}, err
	}
	if response == nil {
		return ZedToken{}, status.Error(codes.Unknown, "SpiceDB returned an empty relationship write response")
	}
	token, err := zedTokenFromResponse(response.GetWrittenAt(), "relationship write")
	if err != nil {
		return ZedToken{}, status.Error(codes.Unknown, err.Error())
	}
	return token, nil
}

func ambiguousRelationshipWriteError(err error) bool {
	switch status.Code(err) {
	case codes.Canceled, codes.DeadlineExceeded, codes.Unknown, codes.Internal, codes.Unavailable:
		return true
	default:
		return false
	}
}

func waitForRelationshipWriteRetry(ctx context.Context, attempt int) error {
	if attempt >= len(relationshipWriteRetryDelays) {
		return nil
	}
	timer := time.NewTimer(relationshipWriteRetryDelays[attempt])
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type expectedRelationshipInspection int

const (
	expectedRelationshipConflictingState expectedRelationshipInspection = iota
	expectedRelationshipDesiredState
	expectedRelationshipPreState
)

func (c *SpiceDBClient) inspectExpectedRelationshipState(
	ctx context.Context,
	states []expectedRelationshipState,
) (expectedRelationshipInspection, ZedToken, error) {
	readAt, err := c.fullyConsistentRelationshipReadToken(ctx)
	if err != nil {
		return expectedRelationshipConflictingState, ZedToken{}, err
	}
	allDesired := true
	allPreState := true
	for _, state := range states {
		exists, readErr := c.relationshipExistsAtExactSnapshot(ctx, state.key, readAt)
		if readErr != nil {
			return expectedRelationshipConflictingState, ZedToken{}, readErr
		}
		allDesired = allDesired && exists == state.desiredExists
		allPreState = allPreState && exists == state.preStateExists
	}
	if allDesired {
		return expectedRelationshipDesiredState, readAt, nil
	}
	if allPreState {
		return expectedRelationshipPreState, readAt, nil
	}
	return expectedRelationshipConflictingState, readAt, nil
}

func (c *SpiceDBClient) fullyConsistentRelationshipReadToken(ctx context.Context) (ZedToken, error) {
	probeCan, err := policyv1.Platform.IsAdmin()
	if err != nil {
		return ZedToken{}, err
	}
	response, err := c.client.CheckPermission(ctx, &v1.CheckPermissionRequest{
		Resource:    &v1.ObjectReference{ObjectType: probeCan.Resource().Type(), ObjectId: probeCan.Resource().ID()},
		Permission:  probeCan.Action().Permission(),
		Subject:     &v1.SubjectReference{Object: &v1.ObjectReference{ObjectType: spiceDBAccountIdentityObjectType, ObjectId: snapshotProbeAccountIdentityID}},
		Consistency: fullyConsistentSpiceDB(),
	})
	if err != nil {
		return ZedToken{}, err
	}
	if response == nil {
		return ZedToken{}, fmt.Errorf("SpiceDB returned an empty consistency probe response")
	}
	return zedTokenFromResponse(response.GetCheckedAt(), "consistency probe")
}

func (c *SpiceDBClient) relationshipExistsAtExactSnapshot(ctx context.Context, key snapshotRelationshipKey, readAt ZedToken) (bool, error) {
	consistency, err := atExactSnapshotSpiceDB(readAt)
	if err != nil {
		return false, err
	}
	stream, err := c.client.ReadRelationships(ctx, &v1.ReadRelationshipsRequest{
		RelationshipFilter: exactRelationshipFilter(key),
		Consistency:        consistency,
		OptionalLimit:      1,
	})
	if err != nil {
		return false, err
	}
	response, err := stream.Recv()
	if err == io.EOF {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if response == nil || response.GetRelationship() == nil {
		return false, fmt.Errorf("SpiceDB returned an empty relationship response")
	}
	return true, nil
}

// SyncAccountIdentityGlobalRole replaces the direct singleton role and verifies
// it at least as fresh as the atomic write.
func (c *SpiceDBClient) SyncAccountIdentityGlobalRole(ctx context.Context, subject AccountIdentitySubject, role policyv1.RoleID) (ZedToken, error) {
	if !role.Valid() {
		return ZedToken{}, fmt.Errorf("unsupported SpiceDB role")
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return ZedToken{}, err
	}
	mutations := make([]policyv1.RelationshipMutation, 0, 3)
	for _, existingRole := range []policyv1.RoleID{policyv1.Role.Admin(), policyv1.Role.Author(), policyv1.Role.User()} {
		var mutation policyv1.RelationshipMutation
		if existingRole == role {
			mutation, err = policyv1.Role.TouchMember(existingRole, actor)
		} else {
			mutation, err = policyv1.Role.DeleteMember(existingRole, actor)
		}
		if err != nil {
			return ZedToken{}, err
		}
		mutations = append(mutations, mutation)
	}
	token, err := c.ApplyRelationships(ctx, mutations...)
	if err != nil {
		return ZedToken{}, err
	}
	actual, found, err := c.ReadDirectGlobalRoleAtLeastAsFresh(ctx, subject, token)
	if err != nil {
		return ZedToken{}, fmt.Errorf("verify global role: %w", err)
	}
	if !found || actual != role {
		return ZedToken{}, fmt.Errorf("verify global role: got %q, want %q", actual.ID(), role.ID())
	}
	return token, nil
}

// DeleteAllAccountIdentityRelationships is the terminal authorization fence
// before the application account anchor is deleted.
func (c *SpiceDBClient) DeleteAllAccountIdentityRelationships(ctx context.Context, subject AccountIdentitySubject) (ZedToken, error) {
	if c == nil || c.client == nil {
		return ZedToken{}, fmt.Errorf("SpiceDB client is not configured")
	}
	if _, err := NewAccountIdentitySubject(subject.ID); err != nil {
		return ZedToken{}, err
	}
	filter := &v1.RelationshipFilter{OptionalSubjectFilter: &v1.SubjectFilter{
		SubjectType: spiceDBAccountIdentityObjectType, OptionalSubjectId: subject.ID.String(),
	}}
	startedAt := time.Now()
	response, err := c.client.DeleteRelationships(ctx, &v1.DeleteRelationshipsRequest{RelationshipFilter: filter})
	if err != nil {
		defaultSpiceDBMetrics.recordWrite(ctx, startedAt, spiceDBWriteOperationSubject, spiceDBOutcomeFailed)
		return ZedToken{}, err
	}
	if response == nil {
		defaultSpiceDBMetrics.recordWrite(ctx, startedAt, spiceDBWriteOperationSubject, spiceDBOutcomeFailed)
		return ZedToken{}, fmt.Errorf("SpiceDB returned an empty account identity relationship delete response")
	}
	if response.GetDeletionProgress() != v1.DeleteRelationshipsResponse_DELETION_PROGRESS_COMPLETE {
		defaultSpiceDBMetrics.recordWrite(ctx, startedAt, spiceDBWriteOperationSubject, spiceDBOutcomeFailed)
		return ZedToken{}, fmt.Errorf("SpiceDB account identity relationship deletion is incomplete")
	}
	token, err := zedTokenFromResponse(response.GetDeletedAt(), "account identity relationship delete")
	if err != nil {
		defaultSpiceDBMetrics.recordWrite(ctx, startedAt, spiceDBWriteOperationSubject, spiceDBOutcomeFailed)
		return ZedToken{}, err
	}
	defaultSpiceDBMetrics.recordWrite(ctx, startedAt, spiceDBWriteOperationSubject, spiceDBOutcomeSucceeded)
	consistency, err := atLeastAsFreshSpiceDB(token)
	if err != nil {
		return ZedToken{}, err
	}
	stream, err := c.client.ReadRelationships(ctx, &v1.ReadRelationshipsRequest{RelationshipFilter: filter, Consistency: consistency})
	if err != nil {
		return ZedToken{}, fmt.Errorf("verify deleted account identity relationships: %w", err)
	}
	item, err := stream.Recv()
	if err == io.EOF {
		return token, nil
	}
	if err != nil {
		return ZedToken{}, fmt.Errorf("verify deleted account identity relationships: %w", err)
	}
	if item != nil && item.GetRelationship() != nil {
		return ZedToken{}, fmt.Errorf("account identity %s still has a SpiceDB relationship", subject.ID)
	}
	return token, nil
}
