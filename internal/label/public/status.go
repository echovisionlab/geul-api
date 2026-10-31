package public

import (
	"github.com/echovisionlab/geul-api/internal/model"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// publicLabelStatus maps a stored Label status to the public API enum. A
// value the API does not know is UNSPECIFIED.
func publicLabelStatus(status model.LabelStatus) openv1.LabelStatus {
	switch status {
	case model.LabelStatusDraft:
		return openv1.LabelStatus_LABEL_STATUS_DRAFT
	case model.LabelStatusPublished:
		return openv1.LabelStatus_LABEL_STATUS_PUBLISHED
	default:
		return openv1.LabelStatus_LABEL_STATUS_UNSPECIFIED
	}
}
