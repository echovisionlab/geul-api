package form

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (s *FormService) appendFormCreatedAudit(ctx context.Context, tx *gorm.DB, formID string) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(
		ctx, tx, s.auditWriter, sharedtelemetry.AuditFormCreated,
		func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewFormCreatedAuditRecord(m, formID)
		},
	)
}

func (s *FormService) appendFormDeletedAudit(ctx context.Context, tx *gorm.DB, formID string) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(
		ctx, tx, s.auditWriter, sharedtelemetry.AuditFormDeleted,
		func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewFormDeletedAuditRecord(m, formID)
		},
	)
}

func (s *FormService) appendFormSettingsAudit(ctx context.Context, tx *gorm.DB, formID string, fields []string) error {
	if s.auditWriter == nil || len(fields) == 0 {
		return nil
	}
	return domainaudit.AppendRequest(
		ctx, tx, s.auditWriter, sharedtelemetry.AuditFormUpdated,
		func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewFormSettingsAuditRecord(m, formID, fields)
		},
	)
}

func (s *FormService) appendFormLifecycleAudit(
	ctx context.Context,
	tx *gorm.DB,
	formID string,
	previous, next sharedtelemetry.AuditState,
) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(
		ctx, tx, s.auditWriter, sharedtelemetry.AuditFormUpdated,
		func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewFormLifecycleAuditRecord(m, formID, previous, next)
		},
	)
}

func (s *FormService) appendFormFeaturedImageAudit(
	ctx context.Context,
	tx *gorm.DB,
	formID, fileID string,
	operation sharedtelemetry.AuditCollectionOperation,
) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(
		ctx, tx, s.auditWriter, sharedtelemetry.AuditFormUpdated,
		func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewFormFeaturedImageAuditRecord(m, formID, fileID, operation)
		},
	)
}

func (s *FormService) appendFormSubmissionDeletedAudit(ctx context.Context, tx *gorm.DB, submissionID string) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(
		ctx, tx, s.auditWriter, sharedtelemetry.AuditFormSubmissionDeleted,
		func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewFormSubmissionDeletedAuditRecord(m, submissionID)
		},
	)
}
