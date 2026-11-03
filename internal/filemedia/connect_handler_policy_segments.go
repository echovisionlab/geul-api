package filemedia

import (
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func downloadPolicyMessage(value *DownloadPolicy) *managev1.FileDownloadPolicy {
	if value == nil {
		return nil
	}
	access, _ := manageFileDownloadAccess(value.Access)
	result := &managev1.FileDownloadPolicy{EntityType: managev1.TranscodeEntityType(value.EntityType), EntityId: value.EntityID, BlockId: value.BlockID, ReferencePath: value.ReferencePath, FileId: value.FileID, Access: access}
	if value.MemberCriteria != nil {
		result.MemberCriteria = make([]*managev1.MemberCriteriaSummary, len(value.MemberCriteria))
		for i, criteria := range value.MemberCriteria {
			if criteria != nil {
				result.MemberCriteria[i] = &managev1.MemberCriteriaSummary{Id: criteria.ID, Name: criteria.Name, Description: criteria.Description, Kind: managev1.MemberCriteriaKind(managev1.MemberCriteriaKind_value[criteria.Kind])}
			}
		}
	}
	return result
}

func manageFileDownloadAccess(access mediaasset.FileDownloadAccess) (managev1.FileDownloadAccess, bool) {
	switch access {
	case mediaasset.FileDownloadAccessDisabled:
		return managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_DISABLED, true
	case mediaasset.FileDownloadAccessPublic:
		return managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_PUBLIC, true
	case mediaasset.FileDownloadAccessAuthenticated:
		return managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_AUTHENTICATED, true
	case mediaasset.FileDownloadAccessRestricted:
		return managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_RESTRICTED, true
	default:
		return managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_UNSPECIFIED, false
	}
}
