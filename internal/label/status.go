package label

import (
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// labelStatusToProto maps a stored Label status to the API enum. A value the
// API does not know is UNSPECIFIED.
func labelStatusToProto(status model.LabelStatus) managev1.LabelStatus {
	switch status {
	case model.LabelStatusDraft:
		return managev1.LabelStatus_LABEL_STATUS_DRAFT
	case model.LabelStatusPublished:
		return managev1.LabelStatus_LABEL_STATUS_PUBLISHED
	default:
		return managev1.LabelStatus_LABEL_STATUS_UNSPECIFIED
	}
}
