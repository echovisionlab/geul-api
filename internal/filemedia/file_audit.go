package filemedia

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func appendFileCreatedAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, id string) error {
	return domainaudit.AppendOptionalRequest(ctx, tx, writer, sharedtelemetry.AuditFileCreated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFileCreatedAuditRecord(m, id)
	})
}

func appendFileDeletedAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, id string) error {
	return domainaudit.AppendOptionalRequest(ctx, tx, writer, sharedtelemetry.AuditFileDeleted, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFileDeletedAuditRecord(m, id)
	})
}

func appendFileRenamedAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, id string) error {
	return domainaudit.AppendOptionalRequest(ctx, tx, writer, sharedtelemetry.AuditFileUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFileRenamedAuditRecord(m, id)
	})
}

func appendFileMovedAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, id, previous, next string) error {
	return domainaudit.AppendOptionalRequest(ctx, tx, writer, sharedtelemetry.AuditFileUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFileMovedAuditRecord(m, id, previous, next)
	})
}

func appendFileFolderCreatedAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, id string) error {
	return domainaudit.AppendOptionalRequest(ctx, tx, writer, sharedtelemetry.AuditFileFolderCreated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFileFolderCreatedAuditRecord(m, id)
	})
}

func appendFileFolderDeletedAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, id string) error {
	return domainaudit.AppendOptionalRequest(ctx, tx, writer, sharedtelemetry.AuditFileFolderDeleted, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFileFolderDeletedAuditRecord(m, id)
	})
}

func appendFileFolderRenamedAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, id string) error {
	return domainaudit.AppendOptionalRequest(ctx, tx, writer, sharedtelemetry.AuditFileFolderUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFileFolderRenamedAuditRecord(m, id)
	})
}

func appendFileFolderMovedAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, id, previous, next string) error {
	return domainaudit.AppendOptionalRequest(ctx, tx, writer, sharedtelemetry.AuditFileFolderUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFileFolderMovedAuditRecord(m, id, previous, next)
	})
}
