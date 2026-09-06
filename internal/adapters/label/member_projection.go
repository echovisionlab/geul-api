package label

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/member"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"gorm.io/gorm"
)

// MemberProjection adapts Member-owned summary projections to Label
// participant reads and mutation responses.
type MemberProjection struct {
	db        *gorm.DB
	cdnDomain string
}

func NewMemberProjection(db *gorm.DB, cdnDomain string) *MemberProjection {
	if db == nil {
		panic("label Member projection: db is required")
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
