package memberadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

type AccountSummaryReader struct{}

var _ memberdomain.AccountSummaryReader = AccountSummaryReader{}

func (AccountSummaryReader) SessionSummaryForMember(
	ctx context.Context, db *gorm.DB, spicedb *auth.SpiceDBClient, memberID string,
) (*managev1.AccountSummary, error) {
	return account.SessionSummaryForMember(ctx, db, spicedb, memberID)
}

func (AccountSummaryReader) SummaryForMember(
	ctx context.Context, db *gorm.DB, spicedb *auth.SpiceDBClient, memberID string,
) (*managev1.AccountSummary, error) {
	return account.SummaryForMember(ctx, db, spicedb, memberID)
}

func (AccountSummaryReader) SummariesForMembers(
	ctx context.Context, db *gorm.DB, spicedb *auth.SpiceDBClient, memberIDs []string,
) (map[string]*managev1.AccountSummary, error) {
	return account.SummariesForMembers(ctx, db, spicedb, memberIDs)
}
