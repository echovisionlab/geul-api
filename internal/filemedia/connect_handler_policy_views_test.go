package filemedia

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestDownloadPolicyViewPreservesSelectorAndCriteriaMetadata(t *testing.T) {
	t.Parallel()
	value := &DownloadPolicy{EntityType: int32(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST), EntityID: "post", BlockID: new(""), ReferencePath: new("file"), FileID: "file", Access: mediaasset.FileDownloadAccessRestricted, MemberCriteria: []*domain.PolicyCriteria{{ID: "criteria", Name: "Members", Description: new(""), Kind: "MEMBER_CRITERIA_KIND_MEMBER_TAGS"}, {ID: "unknown", Kind: "future"}}}
	requireMediaWire(t, &managev1.FileDownloadPolicy{EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, EntityId: "post", BlockId: new(""), ReferencePath: new("file"), FileId: "file", Access: managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_RESTRICTED, MemberCriteria: []*managev1.MemberCriteriaSummary{{Id: "criteria", Name: "Members", Description: new(""), Kind: managev1.MemberCriteriaKind_MEMBER_CRITERIA_KIND_MEMBER_TAGS}, {Id: "unknown"}}}, downloadPolicyMessage(value))
	empty := downloadPolicyMessage(&DownloadPolicy{})
	require.Nil(t, empty.BlockId)
	require.Nil(t, empty.ReferencePath)
	require.Nil(t, empty.MemberCriteria)
	require.Equal(t, managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_UNSPECIFIED, empty.Access)
	require.Nil(t, downloadPolicyMessage(nil))
}
