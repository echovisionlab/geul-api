package filemedia

import (
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func policySegmentSummaryProto(segment domain.PolicySegment) (*managev1.AudienceSegmentSummary, bool) {
	if !segment.Eligible {
		return nil, false
	}
	return &managev1.AudienceSegmentSummary{Id: segment.ID, Name: segment.Name, Description: segment.Description, SegmentType: managev1.SegmentType(managev1.SegmentType_value[segment.Kind])}, true
}
