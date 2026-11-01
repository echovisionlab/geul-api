//go:build integration

package campaign

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

var errUnusedTestDependency = errors.New("test did not configure this Campaign dependency")

// newTestService builds the Campaign management use cases through
// NewService. Fields left unset in deps get stand-ins that fail if a test
// reaches them, and a recording audit appender.
func newTestService(t *testing.T, deps Deps) *Service {
	t.Helper()
	if deps.SpiceDB == nil {
		deps.SpiceDB = &auth.SpiceDBClient{}
	}
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = &contentblock.Store{}
	}
	if deps.Runtime == nil {
		deps.Runtime = newCampaignRuntimeFixture(nil, nil)
	}
	if deps.EmailAuthoring == nil {
		deps.EmailAuthoring = unusedEmailAuthoring{}
	}
	if deps.EmailRendering == nil {
		deps.EmailRendering = unusedEmailRendering{}
	}
	if deps.EmailDelivery == nil {
		deps.EmailDelivery = unusedEmailDelivery{}
	}
	if deps.AudienceTargets == nil {
		deps.AudienceTargets = campaignAudienceTargets{}
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	service, err := NewService(deps)
	require.NoError(t, err)
	return service
}

// newTestInternalService builds the Campaign collaboration and AI document
// use cases through NewInternalService. Members locks real Member rows.
func newTestInternalService(t *testing.T, deps InternalDeps) *InternalService {
	t.Helper()
	if deps.SpiceDB == nil {
		deps.SpiceDB = &auth.SpiceDBClient{}
	}
	if deps.Permissions == nil {
		deps.Permissions = unusedPermissions{}
	}
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = &contentblock.Store{}
	}
	if deps.Checkpoints == nil {
		deps.Checkpoints = unusedCheckpoints{}
	}
	if deps.Members == nil {
		deps.Members = campaignTestContributorMembers{}
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	service, err := NewInternalService(deps)
	require.NoError(t, err)
	return service
}

type unusedEmailAuthoring struct{}

func (unusedEmailAuthoring) LockLayoutsForCampaign(context.Context, *gorm.DB, ...string) (map[string]CampaignLayoutReference, error) {
	return nil, errUnusedTestDependency
}

func (unusedEmailAuthoring) LoadLayoutSnapshot(context.Context, *gorm.DB, string) ([]CampaignLayoutLocaleSnapshot, error) {
	return nil, errUnusedTestDependency
}

type unusedEmailRendering struct{}

func (unusedEmailRendering) BuildRenderData(_ context.Context, _ *gorm.DB, _, _, _ string, input map[string]string) map[string]string {
	return input
}

func (unusedEmailRendering) WrapWithLayout(context.Context, *gorm.DB, string, string, string, map[string]string) (string, error) {
	return "", errUnusedTestDependency
}

func (unusedEmailRendering) RenderVariables(content string, _ map[string]string) string {
	return content
}

func (unusedEmailRendering) NormalizeHTML(html string) string { return html }

func (unusedEmailRendering) PlainText(html string) string { return html }

func (unusedEmailRendering) TestRecipientContext(string) *managev1.SendEmailEvent_TestEmail {
	return nil
}

type unusedEmailDelivery struct{}

func (unusedEmailDelivery) CountRecipients(context.Context, *gorm.DB, CampaignDeliveryTarget) (int64, error) {
	return 0, errUnusedTestDependency
}

func (unusedEmailDelivery) SealRun(context.Context, *gorm.DB, CampaignDeliveryRunDefinition) (CampaignDeliveryRunRef, error) {
	return CampaignDeliveryRunRef{}, errUnusedTestDependency
}

func (unusedEmailDelivery) StartRun(context.Context, *gorm.DB, string, int64, time.Time) error {
	return errUnusedTestDependency
}

func (unusedEmailDelivery) CancelActiveRuns(context.Context, *gorm.DB, string, time.Time) error {
	return errUnusedTestDependency
}

func (unusedEmailDelivery) HasHistory(context.Context, *gorm.DB, string) (bool, error) {
	return false, errUnusedTestDependency
}

func (unusedEmailDelivery) LatestStats(context.Context, *gorm.DB, string) (*CampaignDeliveryStats, error) {
	return nil, errUnusedTestDependency
}

func (unusedEmailDelivery) ListRecipients(context.Context, *gorm.DB, string, int, int) (CampaignDeliveryRecipientPage, error) {
	return CampaignDeliveryRecipientPage{}, errUnusedTestDependency
}

type unusedPermissions struct{}

func (unusedPermissions) Can(context.Context, policyv1.AuthorizationDecision) (bool, error) {
	return false, errUnusedTestDependency
}

type unusedCheckpoints struct{}

func (unusedCheckpoints) RequireCurrentContributors(context.Context, *gorm.DB, intrav1.CollaborationResourceType, string, []string) error {
	return errUnusedTestDependency
}

// campaignMemberContext is a request of the authenticated user that domain
// audit attributes to the user's Member.
func campaignMemberContext(t *testing.T, user *auth.UserInfo) context.Context {
	t.Helper()
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{
		IdentityID: string(user.IdentityID), MemberID: string(user.MemberID), SessionID: string(user.SessionID),
	})
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(context.Background(), request), user)
}
