package public

import (
	"github.com/echovisionlab/geul-api/internal/model"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// formStatusToOpen maps a stored form status to the public API enum. A value
// the API does not know is UNSPECIFIED.
func formStatusToOpen(status model.FormStatus) openv1.FormStatus {
	switch status {
	case model.FormStatusDraft:
		return openv1.FormStatus_FORM_STATUS_DRAFT
	case model.FormStatusPublished:
		return openv1.FormStatus_FORM_STATUS_PUBLISHED
	default:
		return openv1.FormStatus_FORM_STATUS_UNSPECIFIED
	}
}
