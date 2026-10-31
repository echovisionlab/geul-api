package entityparticipant

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/member"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"gorm.io/gorm"
)

// MemberProjection adapts Member-owned summary projections to participant
// reads and mutation responses.
type MemberProjection struct {
	db        *gorm.DB
	cdnDomain string
}

// NewMemberProjection panics without a database; entityName ("artist") names
// the consumer in that panic.
func NewMemberProjection(entityName string, db *gorm.DB, cdnDomain string) *MemberProjection {
	if db == nil {
		panic(entityName + " Member projection: db is required")
	}
	return &MemberProjection{db: db, cdnDomain: cdnDomain}
}

func (p *MemberProjection) LoadMemberSummaries(
	ctx context.Context,
	memberIDs []string,
) (map[string]*commonv1.MemberSummary, error) {
	return member.LoadSummaries(ctx, p.db, p.cdnDomain, memberIDs)
}

func (p *MemberProjection) LoadAuthorizationEligibleMemberSummary(
	ctx context.Context,
	memberID string,
) (*commonv1.MemberSummary, error) {
	return member.LoadAuthorizationEligibleSummary(ctx, p.db, p.cdnDomain, memberID)
}
