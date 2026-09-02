package series

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// The unaudited constructor is retained for focused legacy tests. Production
// always uses NewAuditedSeriesService, which makes every successful mutation
// append its record in the same database transaction.
func (s *SeriesService) appendPostSeriesCreatedAudit(ctx context.Context, tx *gorm.DB, seriesID string) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPostSeriesCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostSeriesCreatedAuditRecord(metadata, seriesID)
	})
}

func (s *SeriesService) appendPostSeriesDeletedAudit(ctx context.Context, tx *gorm.DB, seriesID string) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPostSeriesDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostSeriesDeletedAuditRecord(metadata, seriesID)
	})
}

func (s *SeriesService) appendPostSeriesSourceMetadataAudit(ctx context.Context, tx *gorm.DB, seriesID string, fields []string) error {
	if s.auditWriter == nil || len(fields) == 0 {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPostSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostSeriesSourceMetadataAuditRecord(metadata, seriesID, fields)
	})
}

func (s *SeriesService) appendPostSeriesLifecycleAudit(ctx context.Context, tx *gorm.DB, seriesID string, previous, next sharedtelemetry.AuditState) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPostSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostSeriesLifecycleAuditRecord(metadata, seriesID, previous, next)
	})
}

func (s *SeriesService) appendPostSeriesManagerAudit(ctx context.Context, tx *gorm.DB, seriesID, memberID string, previous, next sharedtelemetry.AuditRelationship) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPostSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostSeriesManagerAuditRecord(metadata, seriesID, memberID, previous, next)
	})
}

func (s *SeriesService) appendPostSeriesMembershipAudit(ctx context.Context, tx *gorm.DB, seriesID, postID, previousSeriesID, nextSeriesID string) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPostSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostSeriesMembershipAuditRecord(metadata, seriesID, postID, previousSeriesID, nextSeriesID)
	})
}

func (s *SeriesService) appendPostSeriesOrderAudit(ctx context.Context, tx *gorm.DB, seriesID string, postIDs []string) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPostSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostSeriesOrderAuditRecord(metadata, seriesID, postIDs)
	})
}

func (s *SeriesService) appendPostSeriesFeaturedImageAudit(ctx context.Context, tx *gorm.DB, seriesID string, operation sharedtelemetry.AuditCollectionOperation, fileID string) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPostSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostSeriesFeaturedImageAuditRecord(metadata, seriesID, operation, fileID)
	})
}

func postSeriesAuditState(status string) sharedtelemetry.AuditState {
	if status == "SERIES_STATUS_PUBLISHED" {
		return sharedtelemetry.AuditStatePublished
	}
	return sharedtelemetry.AuditStateDraft
}
