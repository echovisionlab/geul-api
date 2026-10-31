package audience

import (
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// segmentTypeUnspecified is the API name of an absent segment type. It is
// never stored; a create or update that carries it is rejected.
const segmentTypeUnspecified model.AudienceSegmentType = "SEGMENT_TYPE_UNSPECIFIED"

// segmentTypeToProto maps a stored segment type to the API enum. A value the
// API does not know is UNSPECIFIED.
func segmentTypeToProto(segmentType model.AudienceSegmentType) managev1.SegmentType {
	switch segmentType {
	case model.AudienceSegmentAllMembers:
		return managev1.SegmentType_SEGMENT_TYPE_ALL_MEMBERS
	case model.AudienceSegmentMemberTags:
		return managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS
	case model.AudienceSegmentMembersByFilter:
		return managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER
	default:
		return managev1.SegmentType_SEGMENT_TYPE_UNSPECIFIED
	}
}

// segmentTypeFromProto maps an API segment type to the stored value. A value
// the domain does not store keeps its API name, so validation names it in
// "unknown segment type" and a create recognizes segmentTypeUnspecified.
func segmentTypeFromProto(segmentType managev1.SegmentType) model.AudienceSegmentType {
	switch segmentType {
	case managev1.SegmentType_SEGMENT_TYPE_ALL_MEMBERS:
		return model.AudienceSegmentAllMembers
	case managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS:
		return model.AudienceSegmentMemberTags
	case managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER:
		return model.AudienceSegmentMembersByFilter
	default:
		return model.AudienceSegmentType(segmentType.String())
	}
}
