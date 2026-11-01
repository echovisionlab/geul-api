package filemedia

import (
	"testing"
	"time"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestFolderViewWirePreservesTimestampPresenceAndCreator(t *testing.T) {
	t.Parallel()
	zero := time.Time{}
	at := time.Date(2026, 11, 2, 12, 3, 4, 123456789, time.FixedZone("KST", 9*60*60))
	view := &FolderView{ID: "folder", ParentID: new(""), Name: "Folder", CreatedAt: &zero, UpdatedAt: &at, CreatedByMember: &MemberSummary{ID: "member", Nickname: "deleted", Deleted: true}}
	want := &managev1.FileFolder{Id: "folder", ParentId: new(""), Name: "Folder", CreatedAt: timestamppb.New(zero), UpdatedAt: timestamppb.New(at), CreatedByMember: &commonv1.MemberSummary{Id: "member", Nickname: "deleted", Deleted: true}}
	actualBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(folderViewProto(view))
	require.NoError(t, err)
	wantBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(want)
	require.NoError(t, err)
	require.Equal(t, wantBytes, actualBytes)
	empty := folderViewProto(&FolderView{})
	require.Nil(t, empty.ParentId)
	require.Nil(t, empty.CreatedAt)
	require.Nil(t, empty.UpdatedAt)
	require.Nil(t, empty.CreatedByMember)
}
