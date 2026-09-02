package mediaasset

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestRestrictedFileDownloadMemberMatchesAllMembersSegment(t *testing.T) {
	segment := &model.AudienceSegment{
		SegmentType: managev1.SegmentType_SEGMENT_TYPE_ALL_MEMBERS.String(),
	}

	require.True(t, restrictedFileDownloadMemberMatchesSegment(
		restrictedFileDownloadMemberFacts{MemberID: "active-member"},
		segment,
		nil,
		nil,
		nil,
	))
}
