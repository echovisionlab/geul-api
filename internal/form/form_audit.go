package form

import (
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func formCreatedAudit(formID string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditFormCreated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFormCreatedAuditRecord(m, formID)
	})
}

func formDeletedAudit(formID string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditFormDeleted, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFormDeletedAuditRecord(m, formID)
	})
}

func formSettingsAudit(formID string, fields []string) mutation.Audit {
	if len(fields) == 0 {
		return mutation.Unchanged()
	}
	return mutation.RequestAudit(sharedtelemetry.AuditFormUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFormSettingsAuditRecord(m, formID, fields)
	})
}

func formLifecycleAudit(formID string, previous, next Status) mutation.Audit {
	if previous == next {
		return mutation.Unchanged()
	}
	return mutation.RequestAudit(sharedtelemetry.AuditFormUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFormLifecycleAuditRecord(m, formID, formAuditState(previous), formAuditState(next))
	})
}

func formFeaturedImageAudit(formID, fileID string, operation sharedtelemetry.AuditCollectionOperation) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditFormUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFormFeaturedImageAuditRecord(m, formID, fileID, operation)
	})
}

func formSubmissionDeletedAudit(submissionID string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditFormSubmissionDeleted, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFormSubmissionDeletedAuditRecord(m, submissionID)
	})
}

// formLocaleContentRecord builds the record of one changed Form locale. A
// collaboration save and an AI document edit attribute it to their origin
// Member; an XLIFF import to the request.
func formLocaleContentRecord(formID, locale string, operation sharedtelemetry.AuditItemOperation) domainaudit.Builder {
	return func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFormLocaleContentAuditRecord(metadata, formID, locale, operation)
	}
}

func formAuditState(status Status) sharedtelemetry.AuditState {
	if status == StatusPublished {
		return sharedtelemetry.AuditStatePublished
	}
	return sharedtelemetry.AuditStateDraft
}
