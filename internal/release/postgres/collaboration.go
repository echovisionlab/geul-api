package releasepostgres

import (
	"context"

	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

func (s *store) ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error) {
	return auth.ResolveAuthenticatedPrincipalBySessionID(ctx, s.tx, sessionID)
}

func (s *store) RequireCurrentContributors(ctx context.Context, releaseID string, contributors []string) error {
	return s.ports.Checkpoints.RequireCurrentContributors(
		ctx, s.tx, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_RELEASE, releaseID, contributors,
	)
}

// LockMembers locks the rows and lists them, because PostgreSQL rejects row
// locks on aggregates.
func (s *store) LockMembers(ctx context.Context, memberIDs []string) ([]string, error) {
	var locked []string
	if err := s.tx.WithContext(ctx).Table("member").Clauses(clause.Locking{Strength: "KEY SHARE"}).
		Where("id IN ?", memberIDs).Pluck("id::text", &locked).Error; err != nil {
		return nil, err
	}
	return locked, nil
}

func (s *store) MemberPreferredLocale(ctx context.Context, memberID string) (*string, error) {
	var preferredLocale *string
	if err := s.tx.WithContext(ctx).Model(&model.Member{}).
		Select("preferred_locale").
		Where("id = ?::uuid AND deleted_at IS NULL", memberID).
		Scan(&preferredLocale).Error; err != nil {
		return nil, err
	}
	return preferredLocale, nil
}
