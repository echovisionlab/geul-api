package form

import (
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// formStatusToProto maps a stored form status to the API enum. A value the
// API does not know is UNSPECIFIED.
func formStatusToProto(status model.FormStatus) managev1.FormStatus {
	switch status {
	case model.FormStatusDraft:
		return managev1.FormStatus_FORM_STATUS_DRAFT
	case model.FormStatusPublished:
		return managev1.FormStatus_FORM_STATUS_PUBLISHED
	default:
		return managev1.FormStatus_FORM_STATUS_UNSPECIFIED
	}
}

// formStatusFromProto maps an API form status to the stored value. It
// reports false for UNSPECIFIED and unknown values, which a client may not set.
func formStatusFromProto(status managev1.FormStatus) (model.FormStatus, bool) {
	switch status {
	case managev1.FormStatus_FORM_STATUS_DRAFT:
		return model.FormStatusDraft, true
	case managev1.FormStatus_FORM_STATUS_PUBLISHED:
		return model.FormStatusPublished, true
	default:
		return "", false
	}
}
