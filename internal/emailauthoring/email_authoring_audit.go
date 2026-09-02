package emailauthoring

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func appendEmailTemplateCreatedAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, templateID string) error {
	if writer == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, writer, sharedtelemetry.AuditEmailTemplateCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewEmailTemplateCreatedAuditRecord(metadata, templateID)
	})
}

func appendEmailTemplateDeletedAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, templateID string) error {
	if writer == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, writer, sharedtelemetry.AuditEmailTemplateDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewEmailTemplateDeletedAuditRecord(metadata, templateID)
	})
}

func appendEmailTemplateMetadataAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, templateID string, fields []string) error {
	if writer == nil || len(fields) == 0 {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, writer, sharedtelemetry.AuditEmailTemplateUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewEmailTemplateMetadataAuditRecord(metadata, templateID, fields)
	})
}

func appendEmailTemplateLayoutAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, templateID, previousLayoutID, nextLayoutID string) error {
	if writer == nil || previousLayoutID == nextLayoutID {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, writer, sharedtelemetry.AuditEmailTemplateUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewEmailTemplateLayoutRelationAuditRecord(metadata, templateID, previousLayoutID, nextLayoutID)
	})
}

func appendEmailLayoutCreatedAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, layoutID string) error {
	if writer == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, writer, sharedtelemetry.AuditEmailLayoutCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewEmailLayoutCreatedAuditRecord(metadata, layoutID)
	})
}

func appendEmailLayoutDeletedAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, layoutID string) error {
	if writer == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, writer, sharedtelemetry.AuditEmailLayoutDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewEmailLayoutDeletedAuditRecord(metadata, layoutID)
	})
}

func appendEmailLayoutMetadataAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, layoutID string, fields []string) error {
	if writer == nil || len(fields) == 0 {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, writer, sharedtelemetry.AuditEmailLayoutUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewEmailLayoutMetadataAuditRecord(metadata, layoutID, fields)
	})
}

func appendEmailTemplateLocaleContentAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	templateID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	return domainaudit.AppendMember(ctx, tx, writer, memberID, sharedtelemetry.AuditEmailTemplateUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewEmailTemplateLocaleContentAuditRecord(metadata, templateID, locale, operation)
	})
}

func appendEmailLayoutLocaleContentAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	layoutID string,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	return domainaudit.AppendMember(ctx, tx, writer, memberID, sharedtelemetry.AuditEmailLayoutUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewEmailLayoutLocaleContentAuditRecord(metadata, layoutID, locale, operation)
	})
}

func emailAuthoringLocaleContentOperation(
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

func appendEmailEventMappingAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, eventName, previousTemplateID, nextTemplateID string) error {
	if writer == nil || previousTemplateID == nextTemplateID {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, writer, sharedtelemetry.AuditEmailEventMappingUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewEmailEventMappingTemplateAuditRecord(metadata, eventName, previousTemplateID, nextTemplateID)
	})
}
