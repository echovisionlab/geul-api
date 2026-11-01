package campaign

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainaudit"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// appendCampaignTerminalStatusAudit records a delivery worker's terminal
// Campaign transition as the backend service.
func appendCampaignTerminalStatusAudit(ctx context.Context, joined Joined, campaignID string, previous, next sharedtelemetry.AuditState) error {
	return domainaudit.AppendSystem(ctx, nil, joined.Audit, sharedtelemetry.ServiceBackend, sharedtelemetry.AuditCampaignUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewCampaignStatusLifecycleAuditRecord(m, campaignID, previous, next)
	})
}

func campaignAuditState(status CampaignStatus) sharedtelemetry.AuditState {
	switch CampaignStatus(status) {
	case CampaignStatusScheduled:
		return sharedtelemetry.AuditStateScheduled
	case CampaignStatusSending:
		return sharedtelemetry.AuditStateSending
	case CampaignStatusSent:
		return sharedtelemetry.AuditStateSent
	case CampaignStatusFailed:
		return sharedtelemetry.AuditStateFailed
	default:
		return sharedtelemetry.AuditStateDraft
	}
}
