package programevent

import (
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func programEventAuditState(status Status) sharedtelemetry.AuditState {
	switch status {
	case StatusDraft:
		return sharedtelemetry.AuditStateDraft
	case StatusPublished:
		return sharedtelemetry.AuditStatePublished
	case StatusArchived:
		return sharedtelemetry.AuditStateArchived
	default:
		return sharedtelemetry.AuditStateNone
	}
}

func programEventSeriesAuditState(status Status) sharedtelemetry.AuditState {
	if status == StatusPublished {
		return sharedtelemetry.AuditStatePublished
	}
	return sharedtelemetry.AuditStateDraft
}

// seriesStatusFilterValues maps the series API status names a list filter
// names to their stored values.
var seriesStatusFilterValues = map[string]Status{
	"PROGRAM_EVENT_SERIES_STATUS_DRAFT":     StatusDraft,
	"PROGRAM_EVENT_SERIES_STATUS_PUBLISHED": StatusPublished,
}
