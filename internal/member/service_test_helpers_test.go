package member

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var errUnusedTestDependency = errors.New("test did not configure this Member dependency")

// newTestService builds the Member use cases through NewService. Fields left
// unset in deps get stand-ins that fail if a test reaches them, and a
// recording audit appender.
func newTestService(t *testing.T, deps Deps) *Service {
	t.Helper()
	if deps.SpiceDB == nil {
		deps.SpiceDB = &auth.SpiceDBClient{}
	}
	if deps.Identity == nil {
		deps.Identity = &fakeIdentityManager{}
	}
	if deps.AvatarFiles == nil {
		deps.AvatarFiles = unusedAvatarFiles{}
	}
	if deps.WelcomeEmails == nil {
		deps.WelcomeEmails = unusedWelcomeEmails{}
	}
	if deps.Accounts == nil {
		deps.Accounts = unusedAccounts{}
	}
	if deps.AccountEmails == nil {
		deps.AccountEmails = unusedAccountEmails{}
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	service, err := NewService(deps)
	require.NoError(t, err)
	return service
}

type unusedAvatarFiles struct{}

func (unusedAvatarFiles) PromoteUserAvatarAsset(context.Context, string) (*commonv1.AssetRef, error) {
	return nil, errUnusedTestDependency
}

type unusedWelcomeEmails struct{}

func (unusedWelcomeEmails) PublishSendEmail(context.Context, *managev1.SendEmailEvent) error {
	return errUnusedTestDependency
}

func (unusedWelcomeEmails) PublishSendEmailWithExecutor(context.Context, eventpkg.DBTX, *managev1.SendEmailEvent) error {
	return errUnusedTestDependency
}

type unusedAccounts struct{}

func (unusedAccounts) SessionSummaryForMember(context.Context, *gorm.DB, *auth.SpiceDBClient, string) (*managev1.AccountSummary, error) {
	return nil, errUnusedTestDependency
}

func (unusedAccounts) SummaryForMember(context.Context, *gorm.DB, *auth.SpiceDBClient, string) (*managev1.AccountSummary, error) {
	return nil, errUnusedTestDependency
}

func (unusedAccounts) SummariesForMembers(context.Context, *gorm.DB, *auth.SpiceDBClient, []string) (map[string]*managev1.AccountSummary, error) {
	return nil, errUnusedTestDependency
}

type unusedAccountEmails struct{}

func (unusedAccountEmails) AdminDetails(context.Context, *auth.Identity) (*managev1.AccountAdminDetails, error) {
	return nil, errUnusedTestDependency
}

func (unusedAccountEmails) ResolveDelivery(context.Context, *gorm.DB, auth.IdentityGetter, string) (string, string, string, error) {
	return "", "", "", errUnusedTestDependency
}

func TestNewServiceRequiresEveryCollaborator(t *testing.T) {
	_, err := NewService(Deps{})
	require.EqualError(t, err, "member dependencies are required: AccountEmails, Accounts, Audit, AvatarFiles, DB, Identity, SpiceDB, WelcomeEmails")
}
