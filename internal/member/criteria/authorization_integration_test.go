//go:build integration

package criteria_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// SpiceDB itself answers here: the criteria's policy relationship grants an
// administrator manage, survives archive and restore, and a create whose
// transaction rolls back compensates it.
func TestCriteriaAuthorizationIsSynchronousIntegration(t *testing.T) {
	t.Parallel()
	stack := orytest.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.123")
	require.NoError(t, err)
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(context.Background(), requestContext), admin.AuthUserInfo())
	service := newIntegrationService(t, stack.DB, stack.SpiceDBClient, &audittest.Recorder{})

	created, err := service.CreateCriteria(ctx, membercriteria.CreateCriteriaCommand{
		Name: "Synchronous criteria authorization", Kind: membercriteria.KindFilter,
	})
	require.NoError(t, err)
	requireCriteriaResourcePermission(t, stack.SpiceDBClient, created.Criteria.ID, admin.IdentityID, true)
	_, err = service.ArchiveCriteria(ctx, membercriteria.ArchiveCriteriaCommand{ID: created.Criteria.ID})
	require.NoError(t, err)
	requireCriteriaResourcePermission(t, stack.SpiceDBClient, created.Criteria.ID, admin.IdentityID, true)
	_, err = service.RestoreCriteria(ctx, membercriteria.RestoreCriteriaCommand{ID: created.Criteria.ID})
	require.NoError(t, err)
	requireCriteriaResourcePermission(t, stack.SpiceDBClient, created.Criteria.ID, admin.IdentityID, true)

	failingAudit := &captureFailingCriteriaAuditAppender{}
	_, err = newIntegrationService(t, stack.DB, stack.SpiceDBClient, failingAudit).CreateCriteria(ctx, membercriteria.CreateCriteriaCommand{
		Name: "Compensated criteria authorization", Kind: membercriteria.KindFilter,
	})
	require.Error(t, err)
	require.NotEmpty(t, failingAudit.targetID)
	var failedCriteriaRows int64
	require.NoError(t, stack.DB.Table("member_criteria").Where("id = ?", failingAudit.targetID).Count(&failedCriteriaRows).Error)
	require.Zero(t, failedCriteriaRows)
	requireCriteriaResourcePermission(t, stack.SpiceDBClient, failingAudit.targetID, admin.IdentityID, false)
}

type captureFailingCriteriaAuditAppender struct {
	targetID string
}

func (writer *captureFailingCriteriaAuditAppender) AppendDomainAuditInTransaction(_ context.Context, _ *gorm.DB, record sharedtelemetry.AuditRecord) error {
	writer.targetID = record.TargetID
	return errors.New("audit unavailable")
}

func requireCriteriaResourcePermission(t *testing.T, spiceDB *auth.SpiceDBClient, resourceID, accountIdentityID string, expected bool) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(accountIdentityID)
	require.NoError(t, err)
	can, err := policyv1.MemberCriteria.Manage(resourceID)
	require.NoError(t, err)
	allowed, err := spiceDB.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	require.Equal(t, expected, allowed)
}
