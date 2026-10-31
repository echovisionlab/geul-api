package post

import (
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// postStatusToProto maps a stored Post status to the API enum. A value the
// API does not know is UNSPECIFIED.
func postStatusToProto(status model.PostStatus) managev1.PostStatus {
	switch status {
	case model.PostStatusDraft:
		return managev1.PostStatus_POST_STATUS_DRAFT
	case model.PostStatusScheduled:
		return managev1.PostStatus_POST_STATUS_SCHEDULED
	case model.PostStatusPublished:
		return managev1.PostStatus_POST_STATUS_PUBLISHED
	case model.PostStatusArchived:
		return managev1.PostStatus_POST_STATUS_ARCHIVED
	default:
		return managev1.PostStatus_POST_STATUS_UNSPECIFIED
	}
}
