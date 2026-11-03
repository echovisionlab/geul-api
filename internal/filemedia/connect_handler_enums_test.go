package filemedia

import (
	"strconv"
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestDomainEnumsPreserveStoredNamesAndWireOrdinals(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		names map[int32]string
		label func(int32) string
	}{
		{name: "UploadType", names: managev1.UploadType_name, label: func(value int32) string { return UploadType(value).String() }},
		{name: "EntityType", names: managev1.TranscodeEntityType_name, label: func(value int32) string { return EntityType(value).String() }},
		{name: "IngestSource", names: managev1.FileIngestSource_name, label: func(value int32) string { return IngestSource(value).String() }},
		{name: "IngestMediaKind", names: managev1.FileIngestMediaKind_name, label: func(value int32) string { return IngestMediaKind(value).String() }},
		{name: "IngestFailureReason", names: managev1.FileIngestFailureReason_name, label: func(value int32) string { return IngestFailureReason(value).String() }},
		{name: "ClientMediaKind", names: managev1.ClientMediaKind_name, label: func(value int32) string { return ClientMediaKind(value).String() }},
		{name: "MeshCompressionMethod", names: managev1.MeshOptimizationCompressionMethod_name, label: func(value int32) string { return MeshCompressionMethod(value).String() }},
		{name: "MeshProfile", names: managev1.MeshOptimizationProfile_name, label: func(value int32) string { return MeshProfile(value).String() }},
		{name: "LibrarySortField", names: managev1.FileManagerSortField_name, label: func(value int32) string { return LibrarySortField(value).String() }},
		{name: "SortOrder", names: commonv1.SortOrder_name, label: func(value int32) string { return SortOrder(value).String() }},
		{name: "DownloadAccess", names: managev1.FileDownloadAccess_name, label: func(value int32) string { return DownloadAccess(value).String() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for ordinal, name := range test.names {
				require.Equal(t, name, test.label(ordinal))
			}
			for _, unknown := range []int32{-1, 999} {
				require.Equal(t, strconv.FormatInt(int64(unknown), 10), test.label(unknown))
			}
		})
	}
	for value, name := range managev1.UploadType_name {
		parsed, ok := domain.ParseUploadType(name)
		require.True(t, ok)
		require.EqualValues(t, value, parsed)
		require.True(t, parsed.Valid())
	}
	for value, name := range managev1.TranscodeEntityType_name {
		parsed, ok := domain.ParseEntityType(name)
		require.True(t, ok)
		require.EqualValues(t, value, parsed)
		require.True(t, parsed.Valid())
	}
	_, ok := domain.ParseUploadType("UPLOAD_TYPE_FUTURE")
	require.False(t, ok)
	_, ok = domain.ParseEntityType("TRANSCODE_ENTITY_TYPE_FUTURE")
	require.False(t, ok)
	require.False(t, UploadType(-1).Valid())
	require.False(t, EntityType(999).Valid())
}
