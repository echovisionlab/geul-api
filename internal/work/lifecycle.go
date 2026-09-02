package work

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// RequireExists verifies that the Work aggregate exists for an owning-domain
// consumer that stores a Work reference.
func RequireExists(ctx context.Context, db *gorm.DB, workID string) error {
	var work model.Work
	if err := db.WithContext(ctx).Select("id").Take(&work, "id = ?", workID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.NotFound("work", workID)
		}
		return errs.Internal(err)
	}
	return nil
}

func (s *WorkService) requireWorkViewOrNotFound(ctx context.Context, work model.Work) error {
	return requireWorkPermission(ctx, s.spiceDB, work, policyv1.Work.View, workAuthorizationRead)
}

func (s *WorkService) lockWorkAdmin(ctx context.Context, tx *gorm.DB, workID string) error {
	_, err := requireLockedWorkPermission(ctx, tx, s.spiceDB, workID, policyv1.Work.Manage, workAuthorizationMutation)
	return err
}

func (s *WorkService) appendWorkAudit(
	ctx context.Context,
	tx *gorm.DB,
	build func(sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error),
) error {
	if s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditWorkUpdated, build)
}
