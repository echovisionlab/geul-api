package criteria

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Stored values equal the API enum names, so every API criteria type must
// round-trip; a new API type fails here until it is mapped.
func TestKindMappingCoversEveryAPIType(t *testing.T) {
	t.Parallel()
	for value, name := range managev1.MemberCriteriaKind_name {
		kind := managev1.MemberCriteriaKind(value)
		stored := kindFromProto(kind)
		if string(stored) != name {
			t.Fatalf("%s maps to %q", name, stored)
		}
		if kind == managev1.MemberCriteriaKind_MEMBER_CRITERIA_KIND_UNSPECIFIED {
			if stored != kindUnspecified {
				t.Fatalf("UNSPECIFIED maps to %q", stored)
			}
			continue
		}
		if kindToProto(stored) != kind {
			t.Fatalf("%s maps back to %s", name, kindToProto(stored))
		}
	}
	if got := kindToProto(Kind("MEMBER_CRITERIA_KIND_RETIRED")); got != managev1.MemberCriteriaKind_MEMBER_CRITERIA_KIND_UNSPECIFIED {
		t.Fatalf("unknown stored type mapped to %s", got)
	}
}
