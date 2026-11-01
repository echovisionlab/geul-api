package filemedia

import (
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func folderViewProto(folder *FolderView) *managev1.FileFolder {
	if folder == nil {
		return nil
	}
	result := &managev1.FileFolder{Id: folder.ID, ParentId: folder.ParentID, Name: folder.Name, CreatedByMember: memberSummaryProto(folder.CreatedByMember)}
	if folder.CreatedAt != nil {
		result.CreatedAt = timestamppb.New(*folder.CreatedAt)
	}
	if folder.UpdatedAt != nil {
		result.UpdatedAt = timestamppb.New(*folder.UpdatedAt)
	}
	return result
}
