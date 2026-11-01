package campaign

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

// appendCampaignTerminalStatusAudit records a delivery worker's terminal
// Campaign transition as the backend service.
func appendCampaignTerminalStatusAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, campaignID string, previous, next sharedtelemetry.AuditState) error {
	return domainaudit.AppendSystem(ctx, tx, writer, sharedtelemetry.ServiceBackend, sharedtelemetry.AuditCampaignUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewCampaignStatusLifecycleAuditRecord(m, campaignID, previous, next)
	})
}

func campaignAuditState(status string) sharedtelemetry.AuditState {
	switch model.CampaignStatus(status) {
	case model.CampaignStatusScheduled:
		return sharedtelemetry.AuditStateScheduled
	case model.CampaignStatusSending:
		return sharedtelemetry.AuditStateSending
	case model.CampaignStatusSent:
		return sharedtelemetry.AuditStateSent
	case model.CampaignStatusFailed:
		return sharedtelemetry.AuditStateFailed
	default:
		return sharedtelemetry.AuditStateDraft
	}
}
