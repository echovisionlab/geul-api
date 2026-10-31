package public

import (
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func openProgramEventStatus(status string) openv1.ProgramEventStatus {
	if value, ok := openv1.ProgramEventStatus_value[status]; ok {
		return openv1.ProgramEventStatus(value)
	}
	return openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_UNSPECIFIED
}

func publicProgramEventStatuses() []string {
	return []string{
		openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String(),
		openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String(),
	}
}

func isPublicProgramEventStatus(status string) bool {
	return status == openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String() ||
		status == openv1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String()
}

func openProgramEventSeriesStatus(status string) openv1.ProgramEventSeriesStatus {
	switch status {
	case managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String():
		return openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED
	case managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT.String():
		return openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_DRAFT
	default:
		return openv1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_UNSPECIFIED
	}
}

func openProgramEventLocationMode(mode string) openv1.ProgramEventLocationMode {
	if value, ok := openv1.ProgramEventLocationMode_value[mode]; ok {
		return openv1.ProgramEventLocationMode(value)
	}
	return openv1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_UNSPECIFIED
}

func publicProgramEventLocationModeUsesMapPlace(mode string) bool {
	return mode == managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_MAP_PLACE.String() ||
		mode == managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_HYBRID.String()
}

func openProgramEventTypeStatus(status string) openv1.ProgramEventTypeStatus {
	if value, ok := openv1.ProgramEventTypeStatus_value[status]; ok {
		return openv1.ProgramEventTypeStatus(value)
	}
	return openv1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_UNSPECIFIED
}
