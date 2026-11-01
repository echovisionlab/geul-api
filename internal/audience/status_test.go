package audience

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Stored values equal the API enum names, so every API segment type must
// round-trip; a new API type fails here until it is mapped.
func TestSegmentTypeMappingCoversEveryAPIType(t *testing.T) {
	t.Parallel()
	for value, name := range managev1.SegmentType_name {
		segmentType := managev1.SegmentType(value)
		stored := segmentTypeFromProto(segmentType)
		if string(stored) != name {
			t.Fatalf("%s maps to %q", name, stored)
		}
		if segmentType == managev1.SegmentType_SEGMENT_TYPE_UNSPECIFIED {
			if stored != segmentTypeUnspecified {
				t.Fatalf("UNSPECIFIED maps to %q", stored)
			}
			continue
		}
		if segmentTypeToProto(stored) != segmentType {
			t.Fatalf("%s maps back to %s", name, segmentTypeToProto(stored))
		}
	}
	if got := segmentTypeToProto(SegmentType("SEGMENT_TYPE_RETIRED")); got != managev1.SegmentType_SEGMENT_TYPE_UNSPECIFIED {
		t.Fatalf("unknown stored type mapped to %s", got)
	}
}
