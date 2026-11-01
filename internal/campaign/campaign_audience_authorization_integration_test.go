//go:build integration

package campaign_test

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type captureFailingCampaignAuditAppender struct{ targetID string }

func (w *captureFailingCampaignAuditAppender) AppendDomainAuditInTransaction(_ context.Context, _ *gorm.DB, r sharedtelemetry.AuditRecord) error {
	w.targetID = r.TargetID
	return errors.New("audit unavailable")
}

// This checks real policy grants and revocation across a rolled-back deletion.
func TestCampaignAuthorizationIsSynchronousIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := integrationPrincipal(t, admin.AuthUserInfo())
	service := integrationService(t, db, stack.SpiceDBClient, nil)
	created, err := service.CreateCampaign(ctx, campaign.CreateCampaignCommand{Name: "Synchronous campaign authorization", Subject: "subject", SourceLocale: "en", Target: campaign.CreateCampaignTarget{All: true}})
	require.NoError(t, err)
	permission := func(want bool) {
		can, err := policyv1.Campaign.Manage(created.ID)
		require.NoError(t, err)
		decision, err := auth.AuthorizationDecision(ctx, can)
		require.NoError(t, err)
		allowed, err := stack.SpiceDBClient.Can(ctx, decision)
		require.NoError(t, err)
		require.Equal(t, want, allowed)
	}
	permission(true)
	failing := &captureFailingCampaignAuditAppender{}
	failedService := integrationService(t, db, stack.SpiceDBClient, failing)
	err = failedService.DeleteCampaign(ctx, campaign.DeleteCampaignCommand{ID: created.ID})
	require.Error(t, err)
	require.Equal(t, created.ID, failing.targetID)
	var count int64
	require.NoError(t, db.Table("campaign").Where("id = ?", created.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
	permission(true)
	afterWriteFailure := errors.New("transaction failed after writing policy deletion")
	compensated := integrationService(t, db, stack.SpiceDBClient, nil, func(units campaign.Units) campaign.Units {
		return failedRelationshipUnits{Units: units, Err: afterWriteFailure}
	})
	require.ErrorIs(t, compensated.DeleteCampaign(ctx, campaign.DeleteCampaignCommand{ID: created.ID}), afterWriteFailure)
	permission(true)
	require.NoError(t, service.DeleteCampaign(ctx, campaign.DeleteCampaignCommand{ID: created.ID}))
	permission(false)
}
