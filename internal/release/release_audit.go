package release

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (s *ReleaseService) appendReleaseCreatedAudit(ctx context.Context, tx *gorm.DB, releaseID string) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditReleaseCreated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseCreatedAuditRecord(m, releaseID)
	})
}
func (s *ReleaseService) appendReleaseDeletedAudit(ctx context.Context, tx *gorm.DB, releaseID string) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditReleaseDeleted, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseDeletedAuditRecord(m, releaseID)
	})
}
func (s *ReleaseService) appendReleaseMetadataAudit(ctx context.Context, tx *gorm.DB, releaseID string, fields []string) error {
	if s.auditWriter == nil || len(fields) == 0 {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseMetadataAuditRecord(m, releaseID, fields)
	})
}
func (s *ReleaseService) appendReleaseArtworkAudit(ctx context.Context, tx *gorm.DB, releaseID, fileID string, op sharedtelemetry.AuditCollectionOperation) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseArtworkAuditRecord(m, releaseID, fileID, op)
	})
}
func (s *ReleaseService) appendReleaseLifecycleAudit(ctx context.Context, tx *gorm.DB, releaseID string, previous, next sharedtelemetry.AuditState) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseLifecycleAuditRecord(m, releaseID, previous, next)
	})
}
func releaseAuditState(status string) sharedtelemetry.AuditState {
	if status == "RELEASE_STATUS_PUBLISHED" {
		return sharedtelemetry.AuditStatePublished
	}
	return sharedtelemetry.AuditStateDraft
}
