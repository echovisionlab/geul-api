package legal

import (
	"context"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func legalPolicyAuditType(entityType string) (sharedtelemetry.AuditPolicyType, error) {
	switch entityType {
	case "terms":
		return sharedtelemetry.AuditPolicyTypeTerms, nil
	case "privacy":
		return sharedtelemetry.AuditPolicyTypePrivacy, nil
	default:
		return "", fmt.Errorf("unsupported legal policy type %q", entityType)
	}
}

func appendLegalPolicyIdentityAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	action sharedtelemetry.AuditAction,
	entityType string,
	policyID string,
	version int,
) error {
	if writer == nil {
		return nil
	}
	policyType, err := legalPolicyAuditType(entityType)
	if err != nil {
		return err
	}
	return domainaudit.AppendRequest(ctx, tx, writer, action, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		if action == sharedtelemetry.AuditLegalPolicyCreated {
			return sharedtelemetry.NewLegalPolicyCreatedAuditRecord(metadata, policyID, policyType, int64(version))
		}
		return sharedtelemetry.NewLegalPolicyDeletedAuditRecord(metadata, policyID, policyType, int64(version))
	})
}

// legalLifecycleChange is the lifecycle record of one version.
type legalLifecycleChange struct {
	kind          string
	policyID      string
	version       int
	changedFields []string
	previous      sharedtelemetry.AuditState
	next          sharedtelemetry.AuditState
	effectiveAt   *time.Time
}

func (c legalLifecycleChange) record(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
	policyType, err := legalPolicyAuditType(c.kind)
	if err != nil {
		return sharedtelemetry.AuditRecord{}, err
	}
	return sharedtelemetry.NewLegalPolicyLifecycleAuditRecord(
		metadata, c.policyID, policyType, int64(c.version), c.changedFields, c.previous, c.next, c.effectiveAt,
	)
}

func appendLegalPolicyLifecycleAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	change legalLifecycleChange,
) error {
	return domainaudit.AppendRequest(ctx, tx, writer, sharedtelemetry.AuditLegalPolicyUpdated, change.record)
}

func appendLegalTargetLocaleContentAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	memberID string,
	entityType string,
	policyID string,
	version int,
	locale string,
	operation sharedtelemetry.AuditItemOperation,
) error {
	return domainaudit.AppendMember(ctx, tx, writer, memberID, sharedtelemetry.AuditLegalPolicyUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		switch entityType {
		case "privacy":
			return sharedtelemetry.NewPrivacyLocaleContentAuditRecord(metadata, policyID, int64(version), locale, operation)
		case "terms":
			return sharedtelemetry.NewTermsLocaleContentAuditRecord(metadata, policyID, int64(version), locale, operation)
		default:
			return sharedtelemetry.AuditRecord{}, fmt.Errorf("unsupported legal policy type %q", entityType)
		}
	})
}

func appendLegalRequestTargetLocaleContentAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	entityType string,
	policyID string,
	version int,
	locale string,
) error {
	if writer == nil {
		return fmt.Errorf("%s target-locale audit writer is required", entityType)
	}
	policyType, err := legalPolicyAuditType(entityType)
	if err != nil {
		return err
	}
	return domainaudit.AppendRequest(ctx, tx, writer, sharedtelemetry.AuditLegalPolicyUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		switch policyType {
		case sharedtelemetry.AuditPolicyTypePrivacy:
			return sharedtelemetry.NewPrivacyLocaleContentAuditRecord(metadata, policyID, int64(version), locale, sharedtelemetry.AuditItemOperationUpdated)
		case sharedtelemetry.AuditPolicyTypeTerms:
			return sharedtelemetry.NewTermsLocaleContentAuditRecord(metadata, policyID, int64(version), locale, sharedtelemetry.AuditItemOperationUpdated)
		default:
			return sharedtelemetry.AuditRecord{}, fmt.Errorf("unsupported legal policy type %q", entityType)
		}
	})
}

func legalTargetLocaleContentOperation(
	translationMutation AITranslationMutation,
	previouslyExists bool,
) sharedtelemetry.AuditItemOperation {
	switch translationMutation {
	case AITranslationDelete:
		return sharedtelemetry.AuditItemOperationDeleted
	case AITranslationCreate:
		return sharedtelemetry.AuditItemOperationCreated
	default:
		if !previouslyExists {
			return sharedtelemetry.AuditItemOperationCreated
		}
		return sharedtelemetry.AuditItemOperationUpdated
	}
}
