package filemedia

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestDownloadPolicyViewPreservesSelectorAndSegmentMetadata(t *testing.T) {
	t.Parallel()
	value := &DownloadPolicy{EntityType: int32(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST), EntityID: "post", BlockID: new(""), ReferencePath: new("file"), FileID: "file", Audience: mediaasset.FileDownloadAudienceRestricted, AudienceSegments: []*domain.PolicySegment{{ID: "segment", Name: "Members", Description: new(""), Kind: "SEGMENT_TYPE_MEMBER_TAGS"}, {ID: "unknown", Kind: "future"}}}
	requireMediaWire(t, &managev1.FileDownloadPolicy{EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, EntityId: "post", BlockId: new(""), ReferencePath: new("file"), FileId: "file", Audience: managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED, AudienceSegments: []*managev1.AudienceSegmentSummary{{Id: "segment", Name: "Members", Description: new(""), SegmentType: managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS}, {Id: "unknown"}}}, downloadPolicyMessage(value))
	empty := downloadPolicyMessage(&DownloadPolicy{})
	require.Nil(t, empty.BlockId)
	require.Nil(t, empty.ReferencePath)
	require.Nil(t, empty.AudienceSegments)
	require.Equal(t, managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_UNSPECIFIED, empty.Audience)
	require.Nil(t, downloadPolicyMessage(nil))
}
