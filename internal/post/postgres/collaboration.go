package postpostgres

import (
	"context"

	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identityread"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (s *store) ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error) {
	return auth.ResolveAuthenticatedPrincipalBySessionID(ctx, s.tx, sessionID)
}

func (s *store) LockContributorMembers(ctx context.Context, memberIDs []string) (int, error) {
	var lockedMembers []struct {
		ID string `gorm:"column:id"`
	}
	if err := s.tx.WithContext(ctx).
		Table("member").
		Clauses(clause.Locking{Strength: "KEY SHARE"}).
		Select("id::text").
		Where("id IN ?", memberIDs).
		Find(&lockedMembers).Error; err != nil {
		return 0, err
	}
	return len(lockedMembers), nil
}

func (s *store) ActiveContributorIdentities(ctx context.Context, memberIDs []string) (map[string]string, error) {
	var rows []struct {
		MemberID   string `gorm:"column:member_id"`
		IdentityID string `gorm:"column:identity_id"`
	}
	if err := s.tx.WithContext(ctx).
		Table("member AS member").
		Joins(identityread.JoinLinked("member", "identity")).
		Select("member.id::text AS member_id, member.account_identity_id::text AS identity_id").
		Where("identity.state = ?", "active").
		Where("member.id IN ? AND member.deleted_at IS NULL AND member.onboarded = TRUE AND member.account_identity_id IS NOT NULL AND COALESCE((identity.metadata_admin ->> 'banned')::boolean, false) = FALSE", memberIDs).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	identities := make(map[string]string, len(rows))
	for _, row := range rows {
		identities[row.MemberID] = row.IdentityID
	}
	return identities, nil
}

func (s *store) MemberPreferredLocale(ctx context.Context, memberID string) (*string, error) {
	var preferredLocale *string
	err := s.tx.WithContext(ctx).Model(&model.Member{}).
		Select("preferred_locale").
		Where("id = ?::uuid AND deleted_at IS NULL", memberID).
		Scan(&preferredLocale).Error
	return preferredLocale, err
}

func (s *store) DefaultTranslationLocale(ctx context.Context) (string, error) {
	settings, err := translation.LoadRuntimeSettings(ctx, s.tx)
	if err != nil {
		return "", err
	}
	return settings.DefaultLocale, nil
}

func (s *store) AppendMemberAudit(ctx context.Context, writer domainaudit.Appender, memberID string, action sharedtelemetry.AuditAction, build domainaudit.Builder) error {
	return domainaudit.AppendMember(ctx, s.tx, writer, memberID, action, build)
}
