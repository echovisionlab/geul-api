package work

import (
	"reflect"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestValidateWorkUpdateObservedFieldsRequiresBaselinesForMetadataAndClients(t *testing.T) {
	metadata, err := structpb.NewStruct(map[string]any{"field": "value"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		request *managev1.UpdateWorkRequest
	}{
		{name: "metadata", request: &managev1.UpdateWorkRequest{Metadata: metadata}},
		{name: "clients", request: &managev1.UpdateWorkRequest{Clients: &managev1.WorkClientsUpdate{ClientIds: []string{"client-a"}}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := validateWorkUpdateObservedFields(test.request)
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("error code = %s, want InvalidArgument (err=%v)", connect.CodeOf(err), err)
			}
		})
	}
}

func TestValidateWorkUpdateObservedFieldsAllowsUnrelatedWritesWithoutBaseline(t *testing.T) {
	featured := true
	if err := validateWorkUpdateObservedFields(&managev1.UpdateWorkRequest{Featured: &featured}); err != nil {
		t.Fatalf("scalar-only update unexpectedly requires a baseline: %v", err)
	}
}

func TestMergeWorkMetadataIntentPreservesUnchangedPeerFields(t *testing.T) {
	current := structured.Fields{
		"description": "peer value",
		"published":   false,
		"nested": map[string]interface{}{
			"edited":    "peer value",
			"other":     "peer addition",
			"untouched": "peer value",
		},
	}
	observed := structured.Fields{
		"description": "baseline",
		"published":   false,
		"nested": map[string]interface{}{
			"edited":    "baseline",
			"untouched": "baseline",
		},
	}
	desired := structured.Fields{
		"description": "local value",
		"published":   false,
		"nested": map[string]interface{}{
			"edited":    "local value",
			"untouched": "baseline",
		},
	}

	got := mergeWorkMetadataIntent(current, observed, desired)
	want := structured.Fields{
		"description": "local value",
		"published":   false,
		"nested": map[string]interface{}{
			"edited":    "local value",
			"other":     "peer addition",
			"untouched": "peer value",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged metadata = %#v, want %#v", got, want)
	}
}

func TestMergeWorkMetadataIntentTreatsArrayAsAtomicAndKeepsEmptyValues(t *testing.T) {
	current := structured.Fields{
		"tags":    []interface{}{"peer", "tag"},
		"empty":   "peer value",
		"enabled": true,
	}
	observed := structured.Fields{
		"tags":    []interface{}{"old"},
		"empty":   "old value",
		"enabled": true,
	}
	desired := structured.Fields{
		"tags":    []interface{}{"local", "tags"},
		"empty":   "",
		"enabled": false,
	}

	got := mergeWorkMetadataIntent(current, observed, desired)
	want := structured.Fields{
		"tags":    []interface{}{"local", "tags"},
		"empty":   "",
		"enabled": false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged metadata = %#v, want %#v", got, want)
	}
}

func TestMergeWorkMetadataIntentAppliesExplicitDeletesWithoutDeletingPeerLeaves(t *testing.T) {
	current := structured.Fields{
		"remove": "peer value",
		"nested": map[string]interface{}{
			"remove": "peer value",
			"keep":   "peer value",
			"added":  "peer addition",
		},
	}
	observed := structured.Fields{
		"remove": "baseline",
		"nested": map[string]interface{}{
			"remove": "baseline",
			"keep":   "baseline",
		},
	}
	desired := structured.Fields{
		"nested": map[string]interface{}{
			"keep": "baseline",
		},
	}

	got := mergeWorkMetadataIntent(current, observed, desired)
	want := structured.Fields{
		"nested": map[string]interface{}{
			"keep":  "peer value",
			"added": "peer addition",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged metadata = %#v, want %#v", got, want)
	}
}

func TestMergeWorkMetadataIntentMergesConcurrentNestedObjectAddition(t *testing.T) {
	current := structured.Fields{
		"nested": map[string]interface{}{"peer": "value"},
	}
	observed := structured.Fields{}
	desired := structured.Fields{
		"nested": map[string]interface{}{"local": "value"},
	}

	got := mergeWorkMetadataIntent(current, observed, desired)
	want := structured.Fields{
		"nested": map[string]interface{}{"peer": "value", "local": "value"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged metadata = %#v, want %#v", got, want)
	}
}

func TestMergeWorkClientIntentPreservesPeerMembershipAndAvoidsResurrection(t *testing.T) {
	cases := []struct {
		name     string
		current  []string
		observed []string
		desired  []string
		want     []string
	}{
		{
			name:     "independent additions from two tabs",
			current:  []string{"client-a", "peer-client"},
			observed: []string{"client-a"},
			desired:  []string{"client-a", "client-b"},
			want:     []string{"client-a", "peer-client", "client-b"},
		},
		{
			name:     "local deletion preserves peer additions",
			current:  []string{"client-a", "peer-client"},
			observed: []string{"client-a", "client-b"},
			desired:  []string{"client-a"},
			want:     []string{"client-a", "peer-client"},
		},
		{
			name:     "peer deletion of an untouched baseline ID is not undone",
			current:  []string{"client-b", "peer-client"},
			observed: []string{"client-a", "client-b"},
			desired:  []string{"client-a", "client-b"},
			want:     []string{"client-b", "peer-client"},
		},
		{
			name:     "local reorder preserves peer addition",
			current:  []string{"client-a", "client-b", "peer-client"},
			observed: []string{"client-a", "client-b"},
			desired:  []string{"client-b", "client-a"},
			want:     []string{"client-b", "client-a", "peer-client"},
		},
		{
			name:     "local reorder does not resurrect peer deletion",
			current:  []string{"client-b", "peer-client"},
			observed: []string{"client-a", "client-b"},
			desired:  []string{"client-b", "client-a"},
			want:     []string{"client-b", "peer-client"},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := mergeWorkClientIntent(test.current, test.observed, test.desired)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("merged client IDs = %#v, want %#v", got, test.want)
			}
		})
	}
}
