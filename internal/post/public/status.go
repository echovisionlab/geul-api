package public

import (
	"github.com/echovisionlab/geul-api/internal/model"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// postStatusToProto maps a stored Post status to the public API enum. A
// value the API does not know is UNSPECIFIED.
func postStatusToProto(status model.PostStatus) openv1.PostStatus {
	switch status {
	case model.PostStatusDraft:
		return openv1.PostStatus_POST_STATUS_DRAFT
	case model.PostStatusScheduled:
		return openv1.PostStatus_POST_STATUS_SCHEDULED
	case model.PostStatusPublished:
		return openv1.PostStatus_POST_STATUS_PUBLISHED
	case model.PostStatusArchived:
		return openv1.PostStatus_POST_STATUS_ARCHIVED
	default:
		return openv1.PostStatus_POST_STATUS_UNSPECIFIED
	}
}
