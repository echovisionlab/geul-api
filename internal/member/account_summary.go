package member

import (
	"context"

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
