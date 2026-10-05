package artist

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func appendArtistAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, action sharedtelemetry.AuditAction, build domainaudit.Builder) error {
	if writer == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, writer, action, build)
}

func (s *ArtistService) appendArtistCreatedAudit(ctx context.Context, tx *gorm.DB, id string) error {
	return appendArtistAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditArtistCreated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewArtistCreatedAuditRecord(m, id)
	})
}

func (s *ArtistService) appendArtistDeletedAudit(ctx context.Context, tx *gorm.DB, id string) error {
	return appendArtistAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditArtistDeleted, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewArtistDeletedAuditRecord(m, id)
	})
}

func (s *ArtistService) appendArtistLifecycleAudit(ctx context.Context, tx *gorm.DB, id string, previous, next sharedtelemetry.AuditState) error {
	return appendArtistAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditArtistUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewArtistLifecycleAuditRecord(m, id, previous, next)
	})
}

func (s *ArtistService) appendArtistGalleryAudit(ctx context.Context, tx *gorm.DB, id string, fileIDs []string) error {
	return appendArtistAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditArtistUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewArtistGalleryAuditRecord(m, id, fileIDs)
	})
}

func (s *ArtistService) appendArtistParticipantAudit(ctx context.Context, tx *gorm.DB, id, member string, previous, next sharedtelemetry.AuditRelationship) error {
	return appendArtistAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditArtistUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewArtistParticipantAuditRecord(m, id, member, previous, next)
	})
}
