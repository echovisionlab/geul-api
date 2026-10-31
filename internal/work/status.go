package work

import (
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// workStatusToProto maps a stored Work status to the API enum. A value the
// API does not know is UNSPECIFIED.
func workStatusToProto(status model.WorkStatus) managev1.WorkStatus {
	switch status {
	case model.WorkStatusDraft:
		return managev1.WorkStatus_WORK_STATUS_DRAFT
	case model.WorkStatusPublished:
		return managev1.WorkStatus_WORK_STATUS_PUBLISHED
	case model.WorkStatusArchived:
		return managev1.WorkStatus_WORK_STATUS_ARCHIVED
	default:
		return managev1.WorkStatus_WORK_STATUS_UNSPECIFIED
	}
}

// workStatusValues are the stored statuses, for filters over work.status.
func workStatusValues() []string {
	return []string{string(model.WorkStatusDraft), string(model.WorkStatusPublished), string(model.WorkStatusArchived)}
}
