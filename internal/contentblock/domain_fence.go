package contentblock

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
)

// LockedFence admits only documentID and answers with the domain context the
// caller already authorized under its lock, so the store does not load and
// authorize the domain a second time.
func LockedFence(entity string, documentID uuid.UUID, domain DomainContext) DomainFence {
	return func(_ context.Context, _ *gorm.DB, requested uuid.UUID) (DomainContext, error) {
		if requested != documentID {
			return DomainContext{}, errs.FailedPrecondition(entity + " content document changed; reload before saving")
		}
		return domain, nil
	}
}
