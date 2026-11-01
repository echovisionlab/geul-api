package auth

import (
	"testing"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestSnapshotOfMatchesTheMutationItNames(t *testing.T) {
	actor, err := policyv1.NewAccountIdentityActor("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	touch, err := policyv1.PostSeries.TouchManager("22222222-2222-4222-8222-222222222222", actor)
	if err != nil {
		t.Fatal(err)
	}
	remove, err := policyv1.PostSeries.DeleteManager("22222222-2222-4222-8222-222222222222", actor)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := SnapshotOf(touch); !snapshot.Matches(touch) || !snapshot.Matches(remove) {
		t.Fatalf("snapshot %+v does not match its own relationship", snapshot)
	}
}
