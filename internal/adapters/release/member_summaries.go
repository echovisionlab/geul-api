package release

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/member"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
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

func (a *MemberSummaries) LoadMemberSummaries(ctx context.Context, memberIDs []string) (map[string]*commonv1.MemberSummary, error) {
	return member.LoadSummaries(ctx, a.db, a.cdnDomain, memberIDs)
}
