//go:build integration

package campaign_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/campaign"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/clause"
)

// A fake cannot prove that authorization happens after PostgreSQL releases a root lock.
func TestCampaignMutationsRecheckAdminAfterRootLockIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.SetupOryStackWithLocalKratos(t)
	db := stack.DB
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := integrationPrincipal(t, admin.AuthUserInfo())
	service := integrationService(t, db, stack.SpiceDBClient, nil)
	created, err := service.CreateCampaign(ctx, campaign.CreateCampaignCommand{Name: "authority race", Subject: "subject", SourceLocale: "en", Target: campaign.CreateCampaignTarget{All: true}})
	require.NoError(t, err)
	holder := db.Begin()
	require.NoError(t, holder.Error)
	t.Cleanup(func() { _ = holder.Rollback().Error })
	var locked model.Campaign
	require.NoError(t, holder.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&locked, "id = ?", created.ID).Error)
	done := make(chan error, 1)
	go func() {
		_, err := service.UpdateCampaignName(ctx, campaign.UpdateCampaignNameCommand{ID: created.ID, Name: "denied rename"})
		done <- err
	}()
	testutil.RequireLockWaiters(t, db, 1)
	testutil.GrantIntegrationGlobalRole(t, stack.SpiceDBClient, admin.IdentityID, policyv1.Role.User())
	require.NoError(t, holder.Commit().Error)
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(<-done).Code())
	var stored model.Campaign
	require.NoError(t, db.First(&stored, "id = ?", created.ID).Error)
	require.Equal(t, created.Name, stored.Name)
}
