package page

import (
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// pageStatusToProto maps a stored Page status to the API enum. A value the
// API does not know is UNSPECIFIED.
func pageStatusToProto(status model.PageStatus) managev1.PageStatus {
	switch status {
	case model.PageStatusDraft:
		return managev1.PageStatus_PAGE_STATUS_DRAFT
	case model.PageStatusPublished:
		return managev1.PageStatus_PAGE_STATUS_PUBLISHED
	default:
		return managev1.PageStatus_PAGE_STATUS_UNSPECIFIED
	}
}

// pageStatusFromProto maps an API Page status to the stored value. It
// reports false for UNSPECIFIED and unknown values.
func pageStatusFromProto(status managev1.PageStatus) (model.PageStatus, bool) {
	switch status {
	case managev1.PageStatus_PAGE_STATUS_DRAFT:
		return model.PageStatusDraft, true
	case managev1.PageStatus_PAGE_STATUS_PUBLISHED:
		return model.PageStatusPublished, true
	default:
		return "", false
	}
}
