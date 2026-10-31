package programevent

import (
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// programEventStatusToProto maps a stored event status to the API enum. A
// value the API does not know is UNSPECIFIED.
func programEventStatusToProto(status model.ProgramEventStatus) managev1.ProgramEventStatus {
	switch status {
	case model.ProgramEventStatusDraft:
		return managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT
	case model.ProgramEventStatusPublished:
		return managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED
	case model.ProgramEventStatusArchived:
		return managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED
	default:
		return managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_UNSPECIFIED
	}
}

// programEventStatusFromProto maps an API event status to the stored value. It
// reports false for UNSPECIFIED and unknown values.
func programEventStatusFromProto(status managev1.ProgramEventStatus) (model.ProgramEventStatus, bool) {
	switch status {
	case managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT:
		return model.ProgramEventStatusDraft, true
	case managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED:
		return model.ProgramEventStatusPublished, true
	case managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED:
		return model.ProgramEventStatusArchived, true
	default:
		return "", false
	}
}

// programEventSeriesStatusToProto maps a stored series status, which holds
// the event draft and published values, to the series API enum.
func programEventSeriesStatusToProto(status model.ProgramEventStatus) managev1.ProgramEventSeriesStatus {
	switch status {
	case model.ProgramEventStatusDraft:
		return managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_DRAFT
	case model.ProgramEventStatusPublished:
		return managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED
	default:
		return managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_UNSPECIFIED
	}
}

// programEventSeriesStatusFromProto maps a series API status to the stored
// value. It reports false for UNSPECIFIED and unknown values.
func programEventSeriesStatusFromProto(status managev1.ProgramEventSeriesStatus) (model.ProgramEventStatus, bool) {
	switch status {
	case managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_DRAFT:
		return model.ProgramEventStatusDraft, true
	case managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED:
		return model.ProgramEventStatusPublished, true
	default:
		return "", false
	}
}

// programEventSeriesStatusFilterValues maps the series API status names a
// list filter names to their stored values.
var programEventSeriesStatusFilterValues = map[string]model.ProgramEventStatus{
	"PROGRAM_EVENT_SERIES_STATUS_DRAFT":     model.ProgramEventStatusDraft,
	"PROGRAM_EVENT_SERIES_STATUS_PUBLISHED": model.ProgramEventStatusPublished,
}

// programEventTypeStatusToProto maps a stored type status to the API enum.
func programEventTypeStatusToProto(status model.ProgramEventTypeStatus) managev1.ProgramEventTypeStatus {
	switch status {
	case model.ProgramEventTypeStatusActive:
		return managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE
	case model.ProgramEventTypeStatusInactive:
		return managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_INACTIVE
	default:
		return managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_UNSPECIFIED
	}
}

// programEventTypeStatusFromProto maps an API type status to the stored
// value. It reports false for UNSPECIFIED and unknown values.
func programEventTypeStatusFromProto(status managev1.ProgramEventTypeStatus) (model.ProgramEventTypeStatus, bool) {
	switch status {
	case managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE:
		return model.ProgramEventTypeStatusActive, true
	case managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_INACTIVE:
		return model.ProgramEventTypeStatusInactive, true
	default:
		return "", false
	}
}

// locationModeToProto maps a stored location mode to the API enum.
func locationModeToProto(mode model.ProgramEventLocationMode) managev1.ProgramEventLocationMode {
	switch mode {
	case model.ProgramEventLocationModeMapPlace:
		return managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_MAP_PLACE
	case model.ProgramEventLocationModeOnline:
		return managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE
	case model.ProgramEventLocationModeHybrid:
		return managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_HYBRID
	case model.ProgramEventLocationModeTBA:
		return managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_TBA
	default:
		return managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_UNSPECIFIED
	}
}

// locationModeFromProto maps an API location mode to the stored value.
// UNSPECIFIED is a Map Place; an unknown value is empty, which location
// validation rejects as unsupported.
func locationModeFromProto(mode managev1.ProgramEventLocationMode) model.ProgramEventLocationMode {
	switch mode {
	case managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_UNSPECIFIED,
		managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_MAP_PLACE:
		return model.ProgramEventLocationModeMapPlace
	case managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE:
		return model.ProgramEventLocationModeOnline
	case managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_HYBRID:
		return model.ProgramEventLocationModeHybrid
	case managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_TBA:
		return model.ProgramEventLocationModeTBA
	default:
		return ""
	}
}

func programEventAuditState(status model.ProgramEventStatus) sharedtelemetry.AuditState {
	switch status {
	case model.ProgramEventStatusDraft:
		return sharedtelemetry.AuditStateDraft
	case model.ProgramEventStatusPublished:
		return sharedtelemetry.AuditStatePublished
	case model.ProgramEventStatusArchived:
		return sharedtelemetry.AuditStateArchived
	default:
		return sharedtelemetry.AuditStateNone
	}
}

func programEventSeriesAuditState(status model.ProgramEventStatus) sharedtelemetry.AuditState {
	if status == model.ProgramEventStatusPublished {
		return sharedtelemetry.AuditStatePublished
	}
	return sharedtelemetry.AuditStateDraft
}
