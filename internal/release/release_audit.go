package release

import (
	"github.com/echovisionlab/geul-api/internal/model"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func releaseAuditState(status model.ReleaseStatus) sharedtelemetry.AuditState {
	if status == model.ReleaseStatusPublished {
		return sharedtelemetry.AuditStatePublished
	}
	return sharedtelemetry.AuditStateDraft
}
