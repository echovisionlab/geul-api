package programevent

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/memberref"

	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	"gorm.io/gorm"
)

// CreditMemberSummaries adapts Member-owned summaries to Program Event credits.
type CreditMemberSummaries struct {
	db        *gorm.DB
	cdnDomain string
}

func NewCreditMemberSummaries(db *gorm.DB, cdnDomain string) *CreditMemberSummaries {
	if db == nil {
		panic("Program Event credit Member summaries: db is required")
	}
	return &CreditMemberSummaries{db: db, cdnDomain: cdnDomain}
}

func (a *CreditMemberSummaries) LoadCreditMemberSummaries(
	ctx context.Context,
	memberIDs []string,
) (map[string]*memberref.Summary, error) {
	return memberpostgres.LoadSummaries(ctx, a.db, a.cdnDomain, memberIDs)
}
