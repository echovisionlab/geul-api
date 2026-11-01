package release

import (
	"reflect"
	"testing"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"google.golang.org/protobuf/proto"
)

func TestObservedRelationSnapshotMustBePresent(t *testing.T) {
	if err := requireObservedRelationSnapshot((*Snapshot[string])(nil)); errs.Wrap(err).Code() != connect.CodeInvalidArgument {
		t.Fatalf("missing observed snapshot error code = %v, want InvalidArgument", errs.Wrap(err).Code())
	}
	if err := requireObservedRelationSnapshot(&Snapshot[string]{}); err != nil {
		t.Fatalf("an explicit empty snapshot should be valid: %v", err)
	}
}

func TestMergeObservedIDsPreservesUnobservedAdditions(t *testing.T) {
	removed, added := mergeObservedIDs(
		[]string{"category-a", "category-b", "category-peer"},
		[]string{"category-a", "category-b"},
		[]string{"category-a", "category-new"},
	)

	if !equalIDs(removed, []string{"category-b"}) {
		t.Fatalf("removed = %v, want [category-b]", removed)
	}
	if !equalIDs(added, []string{"category-new"}) {
		t.Fatalf("added = %v, want [category-new]", added)
	}
}

func TestMergeReleaseLabelsUsesObservedAttributeDeltas(t *testing.T) {
	peerValue := "changed by peer"
	baselineValue := "old"
	userValue := "changed by this user"
	current := []ReleaseLabel{
		{LabelID: "label-a", CatalogNumber: &peerValue, SortOrder: 0},
		{LabelID: "label-peer", CatalogNumber: stringPointer("peer catalog"), SortOrder: 1},
	}
	observed := []*ReleaseLabelInput{{LabelID: "label-a", CatalogNumber: &baselineValue}}

	untouched := mergeReleaseLabels(current, observed, []*ReleaseLabelInput{
		{LabelID: "label-a", CatalogNumber: &baselineValue},
		{LabelID: "label-new", CatalogNumber: stringPointer("new catalog")},
	}, nil)
	if len(untouched) != 3 || untouched[0].CatalogNumber == nil || *untouched[0].CatalogNumber != peerValue {
		t.Fatalf("unchanged baseline overwrote a concurrent property or membership: %+v", untouched)
	}
	if untouched[1].LabelID != "label-peer" || untouched[2].LabelID != "label-new" {
		t.Fatalf("concurrent membership order not preserved with appended addition: %+v", untouched)
	}

	changed := mergeReleaseLabels(current[:1], observed, []*ReleaseLabelInput{
		{LabelID: "label-a", CatalogNumber: &userValue},
	}, nil)
	if len(changed) != 1 || changed[0].CatalogNumber == nil || *changed[0].CatalogNumber != userValue {
		t.Fatalf("changed user property did not win: %+v", changed)
	}
}

func TestApplyRelationOrderIntentUsesNextThenPreviousThenAppend(t *testing.T) {
	tests := []struct {
		name   string
		ids    []string
		intent *RelationOrderIntent
		want   []string
	}{
		{
			name: "next anchor preferred",
			ids:  []string{"a", "peer", "b", "moved"},
			intent: &RelationOrderIntent{
				ItemID: "moved", PreviousItemID: stringPointer("a"), NextItemID: stringPointer("b"),
			},
			want: []string{"a", "peer", "moved", "b"},
		},
		{
			name: "previous anchor fallback",
			ids:  []string{"a", "peer", "moved"},
			intent: &RelationOrderIntent{
				ItemID: "moved", PreviousItemID: stringPointer("a"), NextItemID: stringPointer("deleted"),
			},
			want: []string{"a", "moved", "peer"},
		},
		{
			name: "append without surviving anchors",
			ids:  []string{"moved", "peer"},
			intent: &RelationOrderIntent{
				ItemID: "moved", PreviousItemID: stringPointer("deleted-prev"), NextItemID: stringPointer("deleted-next"),
			},
			want: []string{"peer", "moved"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, _ := applyRelationOrderIntent(test.ids, test.intent)
			if !equalIDs(got, test.want) {
				t.Fatalf("order = %v, want %v", got, test.want)
			}
		})
	}
}

func TestMergeReleaseCreditsPreservesStableIDsAndConcurrentCredits(t *testing.T) {
	role := "Vocals"
	newRole := "Producer"
	current := []ReleaseCredit{
		{ID: "credit-stable", CreditRole: &role, SortOrder: 0, CreditedName: stringPointer("A")},
		{ID: "credit-peer", CreditRole: stringPointer("Guitar"), SortOrder: 1, CreditedName: stringPointer("B")},
	}
	observed := []*CreditInput{{
		ID: stringPointer("credit-stable"), CreditRole: &role, CreditedName: stringPointer("A"), SortOrder: 0,
	}}

	merged := mergeReleaseCredits(current, observed, []*CreditInput{{
		ID: stringPointer("credit-stable"), CreditRole: &role, CreditedName: stringPointer("A"), SortOrder: 0,
	}}, nil)
	if len(merged) != 2 || merged[0].ID != "credit-stable" || merged[1].ID != "credit-peer" {
		t.Fatalf("stable and concurrent rows were not preserved: %+v", merged)
	}

	changed := mergeReleaseCredits(current[:1], observed, []*CreditInput{{
		ID: stringPointer("credit-stable"), CreditRole: &newRole, CreditedName: stringPointer("A"), SortOrder: 0,
	}}, nil)
	if len(changed) != 1 || changed[0].ID != "credit-stable" || changed[0].CreditRole == nil || *changed[0].CreditRole != newRole {
		t.Fatalf("edited credit did not preserve its ID and apply changed property: %+v", changed)
	}
}

func TestMergeTrackCreditsPreservesConcurrentRowsAndUntouchedValues(t *testing.T) {
	oldName := "Old"
	peerName := "Changed elsewhere"
	current := []TrackCredit{
		{ID: "credit-stable", TrackID: "track-1", CreditedName: &peerName, SortOrder: 0},
		{ID: "credit-peer", TrackID: "track-1", CreditedName: stringPointer("Peer"), SortOrder: 1},
	}
	observed := []*CreditInput{{ID: stringPointer("credit-stable"), CreditedName: &oldName}}
	merged := mergeTrackCredits("track-1", current, observed, []*CreditInput{
		{ID: stringPointer("credit-stable"), CreditedName: &oldName},
		{CreditedName: stringPointer("New")},
	})
	if len(merged) != 3 || merged[0].ID != "credit-stable" || merged[1].ID != "credit-peer" ||
		merged[0].CreditedName == nil || *merged[0].CreditedName != peerName {
		t.Fatalf("track merge did not preserve concurrent state: %+v", merged)
	}
}

func TestMergeTrackCreditsDoesNotRecreateObservedCreditDeletedByPeer(t *testing.T) {
	current := []TrackCredit{{
		ID: "credit-stable", TrackID: "track-1", CreditedName: stringPointer("Still present"), SortOrder: 0,
	}}
	observed := []*CreditInput{
		{ID: stringPointer("credit-stable"), CreditedName: stringPointer("Still present")},
		{ID: stringPointer("credit-peer-deleted"), CreditedName: stringPointer("Deleted by peer")},
	}
	desired := []*CreditInput{
		{ID: stringPointer("credit-stable"), CreditedName: stringPointer("Still present")},
		{ID: stringPointer("credit-peer-deleted"), CreditedName: stringPointer("Deleted by peer")},
	}

	merged := mergeTrackCredits("track-1", current, observed, desired)
	if len(merged) != 1 || merged[0].ID != "credit-stable" {
		t.Fatalf("stale desired snapshot recreated a peer-deleted credit: %+v", merged)
	}
}

func stringPointer(value string) *string { return &value }

// A delta that sets a credit role the row already holds must not count as a
// change: it would write nothing and still append an "updated" audit record.
func TestMergeTrackCreditsRoleAlreadyAppliedIsUnchanged(t *testing.T) {
	stored := "producer"
	observedRole := "mixer"
	desiredRole := "producer"
	current := []TrackCredit{{ID: "credit-a", TrackID: "track-1", CreditedName: proto.String("A"), CreditRole: &stored}}
	next := mergeTrackCredits(
		"track-1",
		current,
		[]*CreditInput{{ID: proto.String("credit-a"), CreditedName: proto.String("A"), CreditRole: &observedRole}},
		[]*CreditInput{{ID: proto.String("credit-a"), CreditedName: proto.String("A"), CreditRole: &desiredRole}},
	)
	if len(next) != 1 {
		t.Fatalf("merged credits = %d, want 1", len(next))
	}
	if !reflect.DeepEqual(current[0], next[0]) {
		t.Fatalf("credit with the same role value was treated as changed: %+v", next[0])
	}
}
