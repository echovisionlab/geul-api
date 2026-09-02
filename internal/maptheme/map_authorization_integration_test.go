//go:build integration

package maptheme

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type captureFailingMapAuditAppender struct {
	targetID string
}

func (writer *captureFailingMapAuditAppender) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	writer.targetID = record.TargetID
	return errors.New("audit unavailable")
}

func TestMapThemeAuthorizationIsSynchronousAndCompensatedIntegration(t *testing.T) {
	stack := testutil.PrepareOryIntegrationTest(t)
	db := stack.DB
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.121")
	require.NoError(t, err)
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(context.Background(), requestContext), admin.AuthUserInfo())
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(admin.IdentityID))
	require.NoError(t, err)
	service := NewAuditedMapThemeService(db, apitelemetry.NewDurableWriter(db), stack.SpiceDBClient)

	created, err := service.CreateMapTheme(ctx, connect.NewRequest(validCreateMapThemeRequest("Synchronous map theme")))
	require.NoError(t, err)
	requireResourcePermission(
		t,
		stack.SpiceDBClient,
		created.Msg.Id,
		subject,
		true,
	)

	copied, err := service.CopyMapTheme(ctx, connect.NewRequest(&managev1.CopyMapThemeRequest{
		Id: created.Msg.Id, Name: "Synchronous map theme copy",
	}))
	require.NoError(t, err)
	requireResourcePermission(
		t,
		stack.SpiceDBClient,
		copied.Msg.Id,
		subject,
		true,
	)

	_, err = service.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: copied.Msg.Id}))
	require.NoError(t, err)
	requireResourcePermission(
		t,
		stack.SpiceDBClient,
		copied.Msg.Id,
		subject,
		false,
	)

	failingAudit := &captureFailingMapAuditAppender{}
	failing := NewAuditedMapThemeService(db, failingAudit, stack.SpiceDBClient)
	_, err = failing.CreateMapTheme(ctx, connect.NewRequest(validCreateMapThemeRequest("Compensated map theme")))
	require.Error(t, err)
	require.NotEmpty(t, failingAudit.targetID)
	failedCan, err := policyv1.MapTheme.Manage(failingAudit.targetID)
	require.NoError(t, err)
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	require.NoError(t, err)
	allowed, err := stack.SpiceDBClient.CheckActorCan(t.Context(), actor, failedCan)
	require.NoError(t, err)
	require.False(t, allowed)
	var failedRows int64
	require.NoError(t, db.Table("map_theme").Where("id = ?", failingAudit.targetID).Count(&failedRows).Error)
	require.Zero(t, failedRows)

	_, err = failing.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: created.Msg.Id}))
	require.Error(t, err)
	requireResourcePermission(
		t,
		stack.SpiceDBClient,
		created.Msg.Id,
		subject,
		true,
	)
	var preservedRows int64
	require.NoError(t, db.Table("map_theme").Where("id = ?", created.Msg.Id).Count(&preservedRows).Error)
	require.EqualValues(t, 1, preservedRows)
}
