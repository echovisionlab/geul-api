package artisttest

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Authorizer is the artist.Authorizer of a use-case test: the checks and
// writes of an authztest.Checker, the Artists a resource lookup finds and
// the relationship snapshot a deletion reads.
type Authorizer struct {
	*authztest.Checker
	// Resources is what LookupResources returns; LookupErr fails it.
	Resources []string
	LookupErr error
	// Snapshot is what SnapshotResourceRelationshipDescriptors returns.
	Snapshot []policyv1.RelationshipMutation
	// SnapshotErr fails the snapshot read.
	SnapshotErr error
}

var _ artist.Authorizer = (*Authorizer)(nil)

// LookupResources implements artist.Authorizer.
func (a *Authorizer) LookupResources(context.Context, policyv1.ResourceLookup, policyv1.Actor) ([]string, error) {
	if a.LookupErr != nil {
		return nil, a.LookupErr
	}
	return append([]string(nil), a.Resources...), nil
}

// SnapshotResourceRelationshipDescriptors implements artist.Authorizer.
func (a *Authorizer) SnapshotResourceRelationshipDescriptors(context.Context, policyv1.RelationshipSnapshotPlan) ([]auth.RelationshipSnapshot, auth.ZedToken, error) {
	if a.SnapshotErr != nil {
		return nil, auth.ZedToken{}, a.SnapshotErr
	}
	snapshots := make([]auth.RelationshipSnapshot, 0, len(a.Snapshot))
	for _, mutation := range a.Snapshot {
		snapshots = append(snapshots, auth.RelationshipSnapshotOf(mutation))
	}
	return snapshots, auth.ZedToken{}, nil
}
