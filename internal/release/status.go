package release

import (
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// releaseStatusToProto maps a stored Release status to the API enum. A value
// the API does not know is UNSPECIFIED.
func releaseStatusToProto(status model.ReleaseStatus) managev1.ReleaseStatus {
	switch status {
	case model.ReleaseStatusDraft:
		return managev1.ReleaseStatus_RELEASE_STATUS_DRAFT
	case model.ReleaseStatusPublished:
		return managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED
	default:
		return managev1.ReleaseStatus_RELEASE_STATUS_UNSPECIFIED
	}
}

// releaseStatusFromProto maps an API Release status to the stored value. It
// reports false for UNSPECIFIED and unknown values.
func releaseStatusFromProto(status managev1.ReleaseStatus) (model.ReleaseStatus, bool) {
	switch status {
	case managev1.ReleaseStatus_RELEASE_STATUS_DRAFT:
		return model.ReleaseStatusDraft, true
	case managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED:
		return model.ReleaseStatusPublished, true
	default:
		return "", false
	}
}

// releaseTypeToProto maps a stored Release type to the API enum. A value the
// API does not know reads as an album, the column default.
func releaseTypeToProto(releaseType model.ReleaseType) managev1.ReleaseType {
	switch releaseType {
	case model.ReleaseTypeEP:
		return managev1.ReleaseType_RELEASE_TYPE_EP
	case model.ReleaseTypeSingle:
		return managev1.ReleaseType_RELEASE_TYPE_SINGLE
	case model.ReleaseTypeCompilation:
		return managev1.ReleaseType_RELEASE_TYPE_COMPILATION
	default:
		return managev1.ReleaseType_RELEASE_TYPE_ALBUM
	}
}

// releaseTypeFromProto maps an API Release type to the stored value.
// UNSPECIFIED and unknown values store an album, the column default.
func releaseTypeFromProto(releaseType managev1.ReleaseType) model.ReleaseType {
	switch releaseType {
	case managev1.ReleaseType_RELEASE_TYPE_EP:
		return model.ReleaseTypeEP
	case managev1.ReleaseType_RELEASE_TYPE_SINGLE:
		return model.ReleaseTypeSingle
	case managev1.ReleaseType_RELEASE_TYPE_COMPILATION:
		return model.ReleaseTypeCompilation
	default:
		return model.ReleaseTypeAlbum
	}
}
