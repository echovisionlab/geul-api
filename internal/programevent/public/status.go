package public

import (
	"github.com/echovisionlab/geul-api/internal/model"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// publicProgramEventStatuses are the stored statuses a public reader sees.
var publicProgramEventStatuses = []model.ProgramEventStatus{
	model.ProgramEventStatusPublished,
	model.ProgramEventStatusArchived,
}

// openProgramEventStatus maps a stored event status to the public API enum.
// A value the API does not know is UNSPECIFIED.
func openProgramEventStatus(status model.ProgramEventStatus) openv1.ProgramEventStatus {
	switch status {
	case model.ProgramEventStatusDraft:
		return openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT
	case model.ProgramEventStatusPublished:
		return openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED
	case model.ProgramEventStatusArchived:
		return openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED
	default:
		return openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_UNSPECIFIED
	}
}

// isPublicProgramEventStatus reports whether a status filter value names a
// status a public reader sees; the filter value is the stored status name.
func isPublicProgramEventStatus(value string) bool {
	return value == string(model.ProgramEventStatusPublished) || value == string(model.ProgramEventStatusArchived)
}

// openProgramEventSeriesStatus maps a stored series status, which holds the
// event draft and published values, to the public series enum.
func openProgramEventSeriesStatus(status model.ProgramEventStatus) openv1.ProgramEventSeriesStatus {
	switch status {
	case model.ProgramEventStatusPublished:
		return openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED
	case model.ProgramEventStatusDraft:
		return openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_DRAFT
	default:
		return openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_UNSPECIFIED
	}
}

// openProgramEventLocationMode maps a stored location mode to the public
// enum.
func openProgramEventLocationMode(mode model.ProgramEventLocationMode) openv1.ProgramEventLocationMode {
	switch mode {
	case model.ProgramEventLocationModeMapPlace:
		return openv1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_MAP_PLACE
	case model.ProgramEventLocationModeOnline:
		return openv1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE
	case model.ProgramEventLocationModeHybrid:
		return openv1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_HYBRID
	case model.ProgramEventLocationModeTBA:
		return openv1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_TBA
	default:
		return openv1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_UNSPECIFIED
	}
}

// openProgramEventTypeStatus maps a stored type status to the public enum.
func openProgramEventTypeStatus(status model.ProgramEventTypeStatus) openv1.ProgramEventTypeStatus {
	switch status {
	case model.ProgramEventTypeStatusActive:
		return openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE
	case model.ProgramEventTypeStatusInactive:
		return openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_INACTIVE
	default:
		return openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_UNSPECIFIED
	}
}
