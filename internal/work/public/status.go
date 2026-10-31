package public

import (
	"github.com/echovisionlab/geul-api/internal/model"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// workStatusToProto maps a stored Work status to the public API enum. A
// value the API does not know is UNSPECIFIED.
func workStatusToProto(status model.WorkStatus) openv1.WorkStatus {
	switch status {
	case model.WorkStatusDraft:
		return openv1.WorkStatus_WORK_STATUS_DRAFT
	case model.WorkStatusPublished:
		return openv1.WorkStatus_WORK_STATUS_PUBLISHED
	case model.WorkStatusArchived:
		return openv1.WorkStatus_WORK_STATUS_ARCHIVED
	default:
		return openv1.WorkStatus_WORK_STATUS_UNSPECIFIED
	}
}
