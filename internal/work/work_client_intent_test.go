package work

import (
	"reflect"
	"testing"
)

func TestMergeWorkClientIntentPositionsLocalAdditions(t *testing.T) {
	for _, test := range []struct {
		name     string
		current  []string
		observed []string
		desired  []string
		want     []string
	}{
		{"insert middle", []string{"a", "b"}, []string{"a", "b"}, []string{"a", "c", "b"}, []string{"a", "c", "b"}},
		{"insert first", []string{"a", "b"}, []string{"a", "b"}, []string{"c", "a", "b"}, []string{"c", "a", "b"}},
		{"multiple insertions", []string{"a", "b"}, []string{"a", "b"}, []string{"c", "a", "d", "e", "b"}, []string{"c", "a", "d", "e", "b"}},
		{"append preserves peer reorder and addition", []string{"b", "peer", "a"}, []string{"a", "b"}, []string{"a", "b", "c"}, []string{"b", "peer", "a", "c"}},
		{"insertion preserves peer reorder and addition", []string{"b", "peer", "a"}, []string{"a", "b"}, []string{"a", "c", "b"}, []string{"c", "b", "peer", "a"}},
		{"multiple insertions preserve their order across reversed anchors", []string{"b", "a"}, []string{"a", "b"}, []string{"c", "a", "d", "b"}, []string{"b", "c", "d", "a"}},
		{"removed anchor appends without resurrection", []string{"a", "peer"}, []string{"a", "b"}, []string{"a", "c", "d", "b"}, []string{"a", "peer", "c", "d"}},
		{"explicit reorder positions new clients and preserves peer addition", []string{"a", "peer", "b"}, []string{"a", "b"}, []string{"b", "c", "a"}, []string{"b", "c", "a", "peer"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := mergeWorkClientIntent(test.current, test.observed, test.desired)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("merged client IDs = %v, want %v", got, test.want)
			}
		})
	}
}
