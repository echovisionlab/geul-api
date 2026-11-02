package work

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/memberref"

	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"

	memberpublicpostgres "github.com/echovisionlab/geul-api/internal/member/public/postgres"
	"gorm.io/gorm"
)

// MemberSummaries adapts Member-owned projections to Work credit reads.
type MemberSummaries struct {
	db        *gorm.DB
	cdnDomain string
}

func NewMemberSummaries(db *gorm.DB, cdnDomain string) *MemberSummaries {
	if db == nil {
		panic("work member summaries: db is required")
	}
	return &MemberSummaries{db: db, cdnDomain: cdnDomain}
}

func (a *MemberSummaries) LoadMemberSummaries(
	ctx context.Context,
	memberIDs []string,
) (map[string]*memberref.Summary, error) {
	return memberpostgres.LoadSummaries(ctx, a.db, a.cdnDomain, memberIDs)
}

func (a *MemberSummaries) LoadPublicMemberSummaries(
	ctx context.Context,
	memberIDs []string,
) (map[string]*memberref.Summary, error) {
	return memberpublicpostgres.LoadPublicMemberSummaries(ctx, a.db, a.cdnDomain, memberIDs)
}
