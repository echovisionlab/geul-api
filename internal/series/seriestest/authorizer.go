package seriestest

import (
	"context"
	"sync"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/series"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Authorizer is the Series authorization port over an authztest.Checker:
// the checks and relationship writes are the Checker's, the lookups and the
// deletion snapshot are the test's.
type Authorizer struct {
	*authztest.Checker

	mu sync.Mutex
	// Lookup answers LookupResources; nil finds no Series.
	Lookup func(lookup policyv1.ResourceLookup, actor policyv1.Actor) ([]string, error)
	// Relationships answers SnapshotResourceRelationshipDescriptors per
	// Series: the relationships the snapshot finds.
	Relationships map[string][]policyv1.RelationshipMutation
	// SnapshotErr fails every snapshot.
	SnapshotErr error
}

var _ series.Authorizer = (*Authorizer)(nil)

// NewAuthorizer returns the port over checker.
func NewAuthorizer(checker *authztest.Checker) *Authorizer {
	return &Authorizer{Checker: checker, Relationships: map[string][]policyv1.RelationshipMutation{}}
}

// LookupResources implements series.Authorizer.
func (a *Authorizer) LookupResources(_ context.Context, lookup policyv1.ResourceLookup, actor policyv1.Actor) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Lookup == nil {
		return nil, nil
	}
	return a.Lookup(lookup, actor)
}

// SnapshotResourceRelationshipDescriptors implements series.Authorizer.
func (a *Authorizer) SnapshotResourceRelationshipDescriptors(_ context.Context, plan policyv1.RelationshipSnapshotPlan) ([]auth.RelationshipSnapshot, auth.ZedToken, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.SnapshotErr != nil {
		return nil, auth.ZedToken{}, a.SnapshotErr
	}
	var snapshots []auth.RelationshipSnapshot
	for _, relationship := range a.Relationships[plan.Resource().ID()] {
		snapshots = append(snapshots, auth.SnapshotOf(relationship))
	}
	return snapshots, auth.ZedToken{}, nil
}
