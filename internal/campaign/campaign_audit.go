package campaign

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

// Campaign mutation evidence is intentionally kept beside the owning service:
// it is all Member-owned configuration or lifecycle work. Delivery workers use
// the explicit backend helper below only for terminal results.
func (s *CampaignService) appendCampaignCreatedAudit(ctx context.Context, tx *gorm.DB, campaignID string) error {
	return domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditCampaignCreated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewCampaignCreatedAuditRecord(m, campaignID)
	})
}

func (s *CampaignService) appendCampaignDeletedAudit(ctx context.Context, tx *gorm.DB, campaignID string) error {
	return domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditCampaignDeleted, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewCampaignDeletedAuditRecord(m, campaignID)
	})
}

func (s *CampaignService) appendCampaignMetadataAudit(ctx context.Context, tx *gorm.DB, campaignID string, fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	return domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditCampaignUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewCampaignMetadataAuditRecord(m, campaignID, fields)
	})
}

func (s *CampaignService) appendCampaignStatusAudit(ctx context.Context, tx *gorm.DB, campaignID string, previous, next sharedtelemetry.AuditState) error {
	return domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditCampaignUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewCampaignStatusLifecycleAuditRecord(m, campaignID, previous, next)
	})
}

func (s *CampaignService) appendCampaignScheduleAudit(ctx context.Context, tx *gorm.DB, campaignID string, previous, next sharedtelemetry.AuditState, scheduledAtTime time.Time) error {
	return domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditCampaignUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewCampaignScheduleLifecycleAuditRecord(m, campaignID, previous, next, scheduledAtTime)
	})
}

func (s *CampaignService) appendCampaignDeliveryRunAudit(ctx context.Context, tx *gorm.DB, campaignID string, previous, next sharedtelemetry.AuditState, runID string) error {
	return domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditCampaignUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewCampaignDeliveryRunLifecycleAuditRecord(m, campaignID, previous, next, runID)
	})
}

func appendCampaignTerminalStatusAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, campaignID string, previous, next sharedtelemetry.AuditState) error {
	if writer == nil {
		return nil
	}
	return domainaudit.AppendSystem(ctx, tx, writer, sharedtelemetry.ServiceBackend, sharedtelemetry.AuditCampaignUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewCampaignStatusLifecycleAuditRecord(m, campaignID, previous, next)
	})
}

func appendCampaignLocaleContentAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	campaignID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	return domainaudit.AppendMember(ctx, tx, writer, memberID, sharedtelemetry.AuditCampaignUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewCampaignLocaleContentAuditRecord(metadata, campaignID, locale, operation)
	})
}

func campaignLocaleContentOperation(
	source bool,
	create bool,
	remove bool,
	previouslyExists bool,
) sharedtelemetry.AuditItemOperation {
	switch {
	case source:
		return sharedtelemetry.AuditItemOperationUpdated
	case remove:
		return sharedtelemetry.AuditItemOperationDeleted
	case create || !previouslyExists:
		return sharedtelemetry.AuditItemOperationCreated
	default:
		return sharedtelemetry.AuditItemOperationUpdated
	}
}

func campaignAuditState(status string) sharedtelemetry.AuditState {
	switch status {
	case "CAMPAIGN_STATUS_SCHEDULED":
		return sharedtelemetry.AuditStateScheduled
	case "CAMPAIGN_STATUS_SENDING":
		return sharedtelemetry.AuditStateSending
	case "CAMPAIGN_STATUS_SENT":
		return sharedtelemetry.AuditStateSent
	case "CAMPAIGN_STATUS_FAILED":
		return sharedtelemetry.AuditStateFailed
	default:
		return sharedtelemetry.AuditStateDraft
	}
}
