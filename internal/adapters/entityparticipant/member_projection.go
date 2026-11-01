package entityparticipant

import (
	"context"

	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"

	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
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
	return memberpostgres.LoadSummaries(ctx, p.db, p.cdnDomain, memberIDs)
}

func (p *MemberProjection) LoadAuthorizationEligibleMemberSummary(
	ctx context.Context,
	memberID string,
) (*commonv1.MemberSummary, error) {
	return memberpostgres.LoadAuthorizationEligibleSummary(ctx, p.db, p.cdnDomain, memberID)
}

func (p *MemberProjection) LoadAuthorizationEligibleIdentities(
	ctx context.Context,
	memberIDs []string,
) (map[string]string, error) {
	return memberadapter.NewParticipantIdentities(p.db).LoadAuthorizationEligibleIdentities(ctx, memberIDs)
}
