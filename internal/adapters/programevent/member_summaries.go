package programevent

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/member"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
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
) (map[string]*commonv1.MemberSummary, error) {
	return member.LoadSummaries(ctx, a.db, a.cdnDomain, memberIDs)
}
