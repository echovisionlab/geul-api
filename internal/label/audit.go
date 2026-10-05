package label

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (s *LabelService) appendLabelCreatedAudit(ctx context.Context, tx *gorm.DB, id string) error {
	return appendLabelAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditLabelCreated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewLabelCreatedAuditRecord(m, id)
	})
}

func (s *LabelService) appendLabelDeletedAudit(ctx context.Context, tx *gorm.DB, id string) error {
	return appendLabelAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditLabelDeleted, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewLabelDeletedAuditRecord(m, id)
	})
}

func (s *LabelService) appendLabelLifecycleAudit(ctx context.Context, tx *gorm.DB, id string, previous, next sharedtelemetry.AuditState) error {
	return appendLabelAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditLabelUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewLabelLifecycleAuditRecord(m, id, previous, next)
	})
}

func (s *LabelService) appendLabelLogoAudit(ctx context.Context, tx *gorm.DB, id string, slot sharedtelemetry.AuditAssetSlot, op sharedtelemetry.AuditCollectionOperation, asset string) error {
	return appendLabelAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditLabelUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewLabelLogoAuditRecord(m, id, slot, op, asset)
	})
}

func (s *LabelService) appendLabelParticipantAudit(ctx context.Context, tx *gorm.DB, id, member string, previous, next sharedtelemetry.AuditRelationship) error {
	return appendLabelAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditLabelUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewLabelParticipantAuditRecord(m, id, member, previous, next)
	})
}

func appendLabelAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, action sharedtelemetry.AuditAction, build domainaudit.Builder) error {
	if writer == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, writer, action, build)
}
