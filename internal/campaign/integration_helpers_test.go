//go:build integration

package campaign_test

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil/contenttest"

	campaignadapter "github.com/echovisionlab/geul-api/internal/adapters/campaign"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/campaign"
	campaignpostgres "github.com/echovisionlab/geul-api/internal/campaign/postgres"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type unusedCheckpoint struct{}

func (unusedCheckpoint) RequireCurrentContributors(context.Context, *gorm.DB, intrav1.CollaborationResourceType, string, []string) error {
	panic("unused collaboration checkpoint")
}

type unusedPublisher struct{}

func (unusedPublisher) EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error {
	panic("unused campaign publisher")
}
func integrationService(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient, audit domainaudit.Appender, transforms ...func(campaign.Units) campaign.Units) *campaign.Service {
	t.Helper()
	if audit == nil {
		audit = &audittest.Recorder{}
	}
	runtime := campaignadapter.NewRuntime(unusedPublisher{}, &audittest.Recorder{})
	units, err := campaignpostgres.NewUnits(db, campaignpostgres.Ports{ContentBlocks: contenttest.NewEmailContentBlockStore(t), Publisher: runtime, Routes: runtime, SpiceDB: spiceDB, EmailAuthoring: campaignadapter.NewEmailAuthoring(), EmailRendering: campaignadapter.NewEmailRendering(), CriteriaTargets: campaignadapter.NewCriteriaTargets(), Members: memberadapter.ContributorMembers{}, Checkpoints: unusedCheckpoint{}})
	require.NoError(t, err)
	for _, transform := range transforms {
		units = transform(units)
	}
	service, err := campaign.NewService(campaign.Deps{Units: units, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: audit, Runtime: runtime, Clock: time.Now})
	require.NoError(t, err)
	return service
}
func integrationPrincipal(t *testing.T, user *auth.UserInfo) context.Context {
	t.Helper()
	return auth.WithUser(orytest.NewAuditContext(t, user.IdentityID.String(), user.MemberID.String()), user)
}
