//go:build integration

package audience_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type captureFailingAudienceAuditAppender struct {
	targetID string
}

func (writer *captureFailingAudienceAuditAppender) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	writer.targetID = record.TargetID
	return errors.New("audit unavailable")
}

func TestAudienceAuthorizationIsSynchronousIntegration(t *testing.T) {
	db := newAudienceIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.123")
	require.NoError(t, err)
	ctx := auth.WithUser(
		sharedtelemetry.WithRequestContext(context.Background(), requestContext),
		admin.AuthUserInfo(),
	)
	audience := newAudienceServiceForTest(db, stack.SpiceDBClient)
	segment, err := audience.CreateSegment(ctx, connect.NewRequest(&managev1.CreateSegmentRequest{
		Name:        "Synchronous audience authorization",
		SegmentType: managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER,
		Config:      &managev1.SegmentConfig{},
	}))
	require.NoError(t, err)
	requireAudienceResourcePermission(
		t, stack.SpiceDBClient, segment.Msg.Id, admin.IdentityID, true,
	)
	_, err = audience.ArchiveSegment(
		ctx,
		connect.NewRequest(&managev1.ArchiveSegmentRequest{Id: segment.Msg.Id}),
	)
	require.NoError(t, err)
	requireAudienceResourcePermission(
		t, stack.SpiceDBClient, segment.Msg.Id, admin.IdentityID, true,
	)
	_, err = audience.RestoreSegment(
		ctx,
		connect.NewRequest(&managev1.RestoreSegmentRequest{Id: segment.Msg.Id}),
	)
	require.NoError(t, err)
	requireAudienceResourcePermission(
		t, stack.SpiceDBClient, segment.Msg.Id, admin.IdentityID, true,
	)

	failingAudit := &captureFailingAudienceAuditAppender{}
	failingAudience := newAuditedAudienceServiceForTest(
		db,
		failingAudit,
		stack.SpiceDBClient,
	)
	_, err = failingAudience.CreateSegment(ctx, connect.NewRequest(&managev1.CreateSegmentRequest{
		Name:        "Compensated audience authorization",
		SegmentType: managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER,
		Config:      &managev1.SegmentConfig{},
	}))
	require.Error(t, err)
	require.NotEmpty(t, failingAudit.targetID)
	var failedSegmentRows int64
	require.NoError(t, db.Table("audience_segment").
		Where("id = ?", failingAudit.targetID).
		Count(&failedSegmentRows).Error)
	require.Zero(t, failedSegmentRows)
	requireAudienceResourcePermission(
		t, stack.SpiceDBClient, failingAudit.targetID, admin.IdentityID, false,
	)
}

func requireAudienceResourcePermission(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	resourceID string,
	accountIdentityID string,
	expected bool,
) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(accountIdentityID)
	require.NoError(t, err)
	can, err := policyv1.AudienceSegment.Manage(resourceID)
	require.NoError(t, err)
	allowed, err := spiceDB.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	require.Equal(t, expected, allowed)
}
