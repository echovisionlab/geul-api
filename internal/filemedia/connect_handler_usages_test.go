package filemedia

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestUsageAndDeletionImpactWirePreserveOptionalMetadataAndDomainCodes(t *testing.T) {
	t.Parallel()
	usage := &UsageView{Domain: domain.UsageProgramEvent, EntityID: "event", ReferencePath: "props.file", BlockID: new(""), Count: 2, BlockType: new("file"), Title: new(""), Link: new("/events/event")}
	values := []*DeletionImpact{{FileID: "file", TotalUsageCount: 7, DomainCounts: []*UsageDomainCount{{Domain: domain.UsageProgramEvent, Count: 6}, {Domain: 999, Count: 1}}, FirstUsages: []*UsageView{usage}, HasMoreUsages: true}}
	got := &managev1.GetFileDeletionImpactResponse{Impacts: deletionImpactsProto(values)}
	want := &managev1.GetFileDeletionImpactResponse{Impacts: []*managev1.FileDeletionImpact{{FileId: "file", TotalUsageCount: 7, DomainCounts: []*managev1.FileUsageDomainCount{{Domain: managev1.FileUsageDomain_FILE_USAGE_DOMAIN_PROGRAM_EVENT, Count: 6}, {Domain: 999, Count: 1}}, FirstUsages: []*managev1.FileUsage{{Domain: managev1.FileUsageDomain_FILE_USAGE_DOMAIN_PROGRAM_EVENT, EntityId: "event", ReferencePath: "props.file", BlockId: new(""), Count: 2, BlockType: new("file"), Title: new(""), Link: new("/events/event")}}, HasMoreUsages: true}}}
	actualBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(got)
	require.NoError(t, err)
	wantBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(want)
	require.NoError(t, err)
	require.Equal(t, wantBytes, actualBytes)
	empty := fileUsageViewsProto([]*UsageView{{}})[0]
	require.Nil(t, empty.BlockId)
	require.Nil(t, empty.BlockType)
	require.Nil(t, empty.Title)
	require.Nil(t, empty.Link)
}
