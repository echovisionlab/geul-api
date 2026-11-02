package release

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/memberref"

	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	"gorm.io/gorm"
)

// MemberSummaries adapts the Member-owned public summary projection to Track
// credit reads.
type MemberSummaries struct {
	db        *gorm.DB
	cdnDomain string
}

func NewMemberSummaries(db *gorm.DB, cdnDomain string) *MemberSummaries {
	if db == nil {
		panic("release member summaries: db is required")
	}
	return &MemberSummaries{db: db, cdnDomain: cdnDomain}
}

func (a *MemberSummaries) LoadMemberSummaries(ctx context.Context, memberIDs []string) (map[string]*memberref.Summary, error) {
	return memberpostgres.LoadSummaries(ctx, a.db, a.cdnDomain, memberIDs)
}
