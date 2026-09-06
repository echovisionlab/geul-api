package release

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (s *TrackService) appendReleaseTrackAudit(ctx context.Context, tx *gorm.DB, releaseID, itemID string, op sharedtelemetry.AuditItemOperation) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseTrackAuditRecord(m, releaseID, itemID, op)
	})
}

func (s *TrackService) appendReleaseTrackOrderAudit(ctx context.Context, tx *gorm.DB, releaseID string, ids []string) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseTrackOrderAuditRecord(m, releaseID, ids)
	})
}

func (s *TrackService) appendReleaseTrackAudioAudit(ctx context.Context, tx *gorm.DB, releaseID, trackID, fileID string, op sharedtelemetry.AuditCollectionOperation) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseTrackAudioAuditRecord(m, releaseID, trackID, fileID, op)
	})
}
