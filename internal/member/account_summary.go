package member

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

// AccountSummaryReader is the Member-owned port for bounded Account projections.
type AccountSummaryReader interface {
	SessionSummaryForMember(context.Context, *gorm.DB, *auth.SpiceDBClient, string) (*managev1.AccountSummary, error)
	SummaryForMember(context.Context, *gorm.DB, *auth.SpiceDBClient, string) (*managev1.AccountSummary, error)
	SummariesForMembers(
		context.Context, *gorm.DB, *auth.SpiceDBClient, []string,
	) (map[string]*managev1.AccountSummary, error)
}

func (s *MemberService) sessionAccountSummary(ctx context.Context, memberID string) (*managev1.AccountSummary, error) {
	if s.accountSummaryReader == nil {
		return nil, fmt.Errorf("account summary reader is required")
	}
	return s.accountSummaryReader.SessionSummaryForMember(ctx, s.db, s.spicedb, memberID)
}

type AccountEmailProjection interface {
	AdminDetails(context.Context, *auth.Identity) (*managev1.AccountAdminDetails, error)
	ResolveDelivery(
		context.Context, *gorm.DB, auth.IdentityGetter, string,
	) (string, string, string, error)
}

// RegistrationAccountEmailProjection is the narrow Account-owned projection
// Member needs while provisioning the bilateral identity link.
type RegistrationAccountEmailProjection interface {
	PrepareRegistration(
		context.Context, auth.IdentityGetter, string, string,
	) (*auth.Identity, string, []string, error)
	SyncMemberEmailProjection(
		context.Context, *gorm.DB, auth.IdentityGetter, string, *auth.Identity,
	) error
}

func (s *MemberService) accountAdminProjection(
	ctx context.Context, identity *auth.Identity,
) (*managev1.AccountAdminDetails, error) {
	if s.accountEmailProjection == nil {
		return nil, fmt.Errorf("account email projection is required")
	}
	return s.accountEmailProjection.AdminDetails(ctx, identity)
}

func (s *MemberService) accountSummary(ctx context.Context, memberID string) (*managev1.AccountSummary, error) {
	if s.accountSummaryReader == nil {
		return nil, fmt.Errorf("account summary reader is required")
	}
	return s.accountSummaryReader.SummaryForMember(ctx, s.db, s.spicedb, memberID)
}

func (s *MemberService) accountSummaries(
	ctx context.Context, memberIDs []string,
) (map[string]*managev1.AccountSummary, error) {
	if s.accountSummaryReader == nil {
		return nil, fmt.Errorf("account summary reader is required")
	}
	return s.accountSummaryReader.SummariesForMembers(ctx, s.db, s.spicedb, memberIDs)
}
