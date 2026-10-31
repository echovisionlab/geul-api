package public

import (
	"github.com/echovisionlab/geul-api/internal/model"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// releaseStatusToOpen maps a stored Release status to the public API enum.
// A value the API does not know is UNSPECIFIED.
func releaseStatusToOpen(status model.ReleaseStatus) openv1.ReleaseStatus {
	switch status {
	case model.ReleaseStatusDraft:
		return openv1.ReleaseStatus_RELEASE_STATUS_DRAFT
	case model.ReleaseStatusPublished:
		return openv1.ReleaseStatus_RELEASE_STATUS_PUBLISHED
	default:
		return openv1.ReleaseStatus_RELEASE_STATUS_UNSPECIFIED
	}
}

// releaseTypeToOpen maps a stored Release type to the public API enum. A
// value the API does not know is UNSPECIFIED.
func releaseTypeToOpen(releaseType model.ReleaseType) openv1.ReleaseType {
	switch releaseType {
	case model.ReleaseTypeAlbum:
		return openv1.ReleaseType_RELEASE_TYPE_ALBUM
	case model.ReleaseTypeEP:
		return openv1.ReleaseType_RELEASE_TYPE_EP
	case model.ReleaseTypeSingle:
		return openv1.ReleaseType_RELEASE_TYPE_SINGLE
	case model.ReleaseTypeCompilation:
		return openv1.ReleaseType_RELEASE_TYPE_COMPILATION
	default:
		return openv1.ReleaseType_RELEASE_TYPE_UNSPECIFIED
	}
}
