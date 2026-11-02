package release

import sharedtelemetry "github.com/echovisionlab/geul-telemetry"

func releaseAuditState(status Status) sharedtelemetry.AuditState {
	if status == StatusPublished {
		return sharedtelemetry.AuditStatePublished
	}
	return sharedtelemetry.AuditStateDraft
}
