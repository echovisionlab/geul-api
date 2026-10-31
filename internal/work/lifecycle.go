package work

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// RequireExists verifies that the Work aggregate exists for an owning-domain
// consumer that stores a Work reference.
func RequireExists(ctx context.Context, db *gorm.DB, workID string) error {
	var work model.Work
	if err := db.WithContext(ctx).Select("id").Take(&work, "id = ?", workID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("work", workID)
		}
		return err
	}
	return nil
}

func (s *Service) requireWorkViewOrNotFound(ctx context.Context, work model.Work) error {
	return requireWorkPermission(ctx, s.spiceDB, work, policyv1.Work.View, workAuthorizationRead)
}

// workUpdatedAudit names a Work update record attributed to the request's
// member actor.
func workUpdatedAudit(build domainaudit.Builder) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditWorkUpdated, build)
}
