package programevent

import (
	"context"

	"gorm.io/gorm"

	memberpublic "github.com/echovisionlab/geul-api/internal/member/public"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// PublicCreditMemberSummaries adapts public Member summaries to Program Event credits.
type PublicCreditMemberSummaries struct {
	db        *gorm.DB
	cdnDomain string
}

func NewPublicCreditMemberSummaries(db *gorm.DB, cdnDomain string) *PublicCreditMemberSummaries {
	if db == nil {
		panic("Program Event public credit Member summaries: db is required")
	}
	return &PublicCreditMemberSummaries{db: db, cdnDomain: cdnDomain}
}

func (a *PublicCreditMemberSummaries) LoadPublicCreditMemberSummaries(
	ctx context.Context,
	memberIDs []string,
) (map[string]*commonv1.MemberSummary, error) {
	return memberpublic.LoadPublicMemberSummaries(ctx, a.db, a.cdnDomain, memberIDs)
}
