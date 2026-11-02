package memberadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/identityread"
	"gorm.io/gorm"
)

// ParticipantIdentities resolves the Identities through which Members act
// as Artist and Label participants.
type ParticipantIdentities struct{ db *gorm.DB }

// NewParticipantIdentities returns the participant Identity reader.
func NewParticipantIdentities(db *gorm.DB) ParticipantIdentities {
	return ParticipantIdentities{db: db}
}

// LoadAuthorizationEligibleIdentities maps each Member of memberIDs that is
// active and onboarded, and whose linked Identity is active and not banned,
// to that Identity.
func (p ParticipantIdentities) LoadAuthorizationEligibleIdentities(ctx context.Context, memberIDs []string) (map[string]string, error) {
	identities := make(map[string]string, len(memberIDs))
	if len(memberIDs) == 0 {
		return identities, nil
	}
	var rows []struct {
		MemberID   string `gorm:"column:member_id"`
		IdentityID string `gorm:"column:identity_id"`
	}
	if err := p.db.WithContext(ctx).Raw(`
		SELECT member.id::text AS member_id, identity.id::text AS identity_id
		FROM member
		`+identityread.JoinLinked("member", "identity")+`
		WHERE member.id IN ?
		  AND member.deleted_at IS NULL
		  AND member.onboarded = TRUE
		  AND identity.state = 'active'
		  AND LOWER(COALESCE(identity.metadata_admin ->> 'banned', 'false')) NOT IN ('true', '1')
	`, memberIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		identities[row.MemberID] = row.IdentityID
	}
	return identities, nil
}
