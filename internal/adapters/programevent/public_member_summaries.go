package programevent

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/memberref"

	memberpublicpostgres "github.com/echovisionlab/geul-api/internal/member/public/postgres"
	"gorm.io/gorm"
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
) (map[string]*memberref.Summary, error) {
	return memberpublicpostgres.LoadPublicMemberSummaries(ctx, a.db, a.cdnDomain, memberIDs)
}
