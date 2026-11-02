package filemedia

import (
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func downloadPolicyMessage(value *DownloadPolicy) *managev1.FileDownloadPolicy {
	if value == nil {
		return nil
	}
	audience, _ := manageFileDownloadAudience(value.Audience)
	result := &managev1.FileDownloadPolicy{EntityType: managev1.TranscodeEntityType(value.EntityType), EntityId: value.EntityID, BlockId: value.BlockID, ReferencePath: value.ReferencePath, FileId: value.FileID, Audience: audience}
	if value.AudienceSegments != nil {
		result.AudienceSegments = make([]*managev1.AudienceSegmentSummary, len(value.AudienceSegments))
		for i, segment := range value.AudienceSegments {
			if segment != nil {
				result.AudienceSegments[i] = &managev1.AudienceSegmentSummary{Id: segment.ID, Name: segment.Name, Description: segment.Description, SegmentType: managev1.SegmentType(managev1.SegmentType_value[segment.Kind])}
			}
		}
	}
	return result
}

func manageFileDownloadAudience(audience mediaasset.FileDownloadAudience) (managev1.FileDownloadAudience, bool) {
	switch audience {
	case mediaasset.FileDownloadAudienceDisabled:
		return managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_DISABLED, true
	case mediaasset.FileDownloadAudiencePublic:
		return managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_PUBLIC, true
	case mediaasset.FileDownloadAudienceAuthenticated:
		return managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_AUTHENTICATED, true
	case mediaasset.FileDownloadAudienceRestricted:
		return managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED, true
	default:
		return managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_UNSPECIFIED, false
	}
}
