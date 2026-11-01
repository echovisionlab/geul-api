package filemedia

import (
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func fileCreatedAudit(id string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditFileCreated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFileCreatedAuditRecord(m, id)
	})
}

func fileDeletedAudit(id string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditFileDeleted, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFileDeletedAuditRecord(m, id)
	})
}

func fileRenamedAudit(id string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditFileUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFileRenamedAuditRecord(m, id)
	})
}

func fileMovedAudit(id, previous, next string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditFileUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFileMovedAuditRecord(m, id, previous, next)
	})
}

func fileFolderCreatedAudit(id string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditFileFolderCreated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFileFolderCreatedAuditRecord(m, id)
	})
}

func fileFolderDeletedAudit(id string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditFileFolderDeleted, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFileFolderDeletedAuditRecord(m, id)
	})
}

func fileFolderRenamedAudit(id string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditFileFolderUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFileFolderRenamedAuditRecord(m, id)
	})
}

func fileFolderMovedAudit(id, previous, next string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditFileFolderUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFileFolderMovedAuditRecord(m, id, previous, next)
	})
}
