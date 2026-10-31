package legal

import (
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
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

// policyIdentityAudit names the record of a created or deleted version.
func policyIdentityAudit(action sharedtelemetry.AuditAction, kind *policyKind, row policyRow) mutation.Audit {
	return mutation.RequestAudit(action, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		policyType, err := legalPolicyAuditType(kind.entity())
		if err != nil {
			return sharedtelemetry.AuditRecord{}, err
		}
		if action == sharedtelemetry.AuditLegalPolicyCreated {
			return sharedtelemetry.NewLegalPolicyCreatedAuditRecord(metadata, row.ID, policyType, int64(row.Version))
		}
		return sharedtelemetry.NewLegalPolicyDeletedAuditRecord(metadata, row.ID, policyType, int64(row.Version))
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

func (c legalLifecycleChange) audit() mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditLegalPolicyUpdated, c.record)
}

// legalLocaleContentChange is the record of a version's content in one
// locale. memberID attributes it to the contributing Member; without one the
// request's Member is the actor.
type legalLocaleContentChange struct {
	kind      string
	policyID  string
	version   int
	locale    string
	operation sharedtelemetry.AuditItemOperation
	memberID  string
}

func (c legalLocaleContentChange) record(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
	switch c.kind {
	case "privacy":
		return sharedtelemetry.NewPrivacyLocaleContentAuditRecord(metadata, c.policyID, int64(c.version), c.locale, c.operation)
	case "terms":
		return sharedtelemetry.NewTermsLocaleContentAuditRecord(metadata, c.policyID, int64(c.version), c.locale, c.operation)
	default:
		return sharedtelemetry.AuditRecord{}, fmt.Errorf("unsupported legal policy type %q", c.kind)
	}
}

func (c legalLocaleContentChange) audit() mutation.Audit {
	if c.memberID == "" {
		return mutation.RequestAudit(sharedtelemetry.AuditLegalPolicyUpdated, c.record)
	}
	return mutation.MemberAudit(c.memberID, sharedtelemetry.AuditLegalPolicyUpdated, c.record)
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
