//go:build integration

package audience_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	audiencedomain "github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// SpiceDB itself answers here: the segment's policy relationship grants an
// administrator manage, survives archive and restore, and a create whose
// transaction rolls back compensates it.
func TestAudienceAuthorizationIsSynchronousIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.123")
	require.NoError(t, err)
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(context.Background(), requestContext), admin.AuthUserInfo())
	service := newIntegrationService(t, stack.DB, stack.SpiceDBClient, &audittest.Recorder{})

	created, err := service.CreateSegment(ctx, audiencedomain.CreateSegmentCommand{
		Name: "Synchronous audience authorization", SegmentType: audiencedomain.SegmentMembersByFilter,
	})
	require.NoError(t, err)
	requireAudienceResourcePermission(t, stack.SpiceDBClient, created.Segment.ID, admin.IdentityID, true)
	_, err = service.ArchiveSegment(ctx, audiencedomain.ArchiveSegmentCommand{ID: created.Segment.ID})
	require.NoError(t, err)
	requireAudienceResourcePermission(t, stack.SpiceDBClient, created.Segment.ID, admin.IdentityID, true)
	_, err = service.RestoreSegment(ctx, audiencedomain.RestoreSegmentCommand{ID: created.Segment.ID})
	require.NoError(t, err)
	requireAudienceResourcePermission(t, stack.SpiceDBClient, created.Segment.ID, admin.IdentityID, true)

	failingAudit := &captureFailingAudienceAuditAppender{}
	_, err = newIntegrationService(t, stack.DB, stack.SpiceDBClient, failingAudit).CreateSegment(ctx, audiencedomain.CreateSegmentCommand{
		Name: "Compensated audience authorization", SegmentType: audiencedomain.SegmentMembersByFilter,
	})
	require.Error(t, err)
	require.NotEmpty(t, failingAudit.targetID)
	var failedSegmentRows int64
	require.NoError(t, stack.DB.Table("audience_segment").Where("id = ?", failingAudit.targetID).Count(&failedSegmentRows).Error)
	require.Zero(t, failedSegmentRows)
	requireAudienceResourcePermission(t, stack.SpiceDBClient, failingAudit.targetID, admin.IdentityID, false)
}

type captureFailingAudienceAuditAppender struct {
	targetID string
}

func (writer *captureFailingAudienceAuditAppender) AppendDomainAuditInTransaction(_ context.Context, _ *gorm.DB, record sharedtelemetry.AuditRecord) error {
	writer.targetID = record.TargetID
	return errors.New("audit unavailable")
}

func requireAudienceResourcePermission(t *testing.T, spiceDB *auth.SpiceDBClient, resourceID, accountIdentityID string, expected bool) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(accountIdentityID)
	require.NoError(t, err)
	can, err := policyv1.AudienceSegment.Manage(resourceID)
	require.NoError(t, err)
	allowed, err := spiceDB.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	require.Equal(t, expected, allowed)
}
