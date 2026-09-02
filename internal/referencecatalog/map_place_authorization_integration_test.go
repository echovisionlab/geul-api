//go:build integration

package referencecatalog

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

type mapPlaceTestAssets struct{}

func (mapPlaceTestAssets) LockForAttachment(context.Context, *gorm.DB, []string) error {
	return nil
}
func (mapPlaceTestAssets) BindReady(context.Context, *gorm.DB, AssetBinding) (*commonv1.AssetRef, error) {
	return nil, nil
}
func (mapPlaceTestAssets) Release(context.Context, *gorm.DB, AssetRelease) error { return nil }
func (mapPlaceTestAssets) ReadyRef(context.Context, *gorm.DB, AssetSource) (*commonv1.AssetRef, error) {
	return nil, nil
}

type mapPlaceTestMembers struct{ db *gorm.DB }

func (members mapPlaceTestMembers) Resolve(ctx context.Context, db *gorm.DB, ids []string) (map[string]*commonv1.MemberSummary, error) {
	result := make(map[string]*commonv1.MemberSummary, len(ids))
	var rows []struct {
		ID       string `gorm:"column:id"`
		Nickname string `gorm:"column:nickname"`
	}
	if err := db.WithContext(ctx).Table("member").Select("id::text, nickname").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ID] = &commonv1.MemberSummary{Id: row.ID, Nickname: row.Nickname}
	}
	return result, nil
}

type failingMapPlaceAuditAppender struct{}

func (failingMapPlaceAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return errors.New("audit unavailable")
}

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start reference catalog integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close reference catalog integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func TestMapPlaceAuthorizationIsSynchronousIntegration(t *testing.T) {
	stack := testutil.PrepareOryIntegrationTest(t)
	db := stack.DB
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.122")
	require.NoError(t, err)
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(context.Background(), requestContext), admin.AuthUserInfo())
	service := NewAuditedMapPlaceService(
		db,
		apitelemetry.NewDurableWriter(db),
		mapPlaceTestAssets{},
		mapPlaceTestMembers{db: db},
		stack.SpiceDBClient,
	)

	created, err := service.CreateMapPlace(ctx, connect.NewRequest(&managev1.CreateMapPlaceRequest{
		Name: "Synchronous map place", Address: "Causal Road", Lat: 37.5, Lng: 127.0,
	}))
	require.NoError(t, err)
	requireMapPlacePermission(t, stack.SpiceDBClient, created.Msg.Id, admin.IdentityID, true)

	failing := NewAuditedMapPlaceService(
		db, failingMapPlaceAuditAppender{}, mapPlaceTestAssets{}, mapPlaceTestMembers{db: db}, stack.SpiceDBClient,
	)
	_, err = failing.DeleteMapPlace(ctx, connect.NewRequest(&managev1.DeleteMapPlaceRequest{Id: created.Msg.Id}))
	require.Error(t, err)
	requireMapPlacePermission(t, stack.SpiceDBClient, created.Msg.Id, admin.IdentityID, true)
	var preservedRows int64
	require.NoError(t, db.Table("map_place").Where("id = ?", created.Msg.Id).Count(&preservedRows).Error)
	require.EqualValues(t, 1, preservedRows)

	_, err = service.DeleteMapPlace(ctx, connect.NewRequest(&managev1.DeleteMapPlaceRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	requireMapPlacePermission(t, stack.SpiceDBClient, created.Msg.Id, admin.IdentityID, false)
}

func requireMapPlacePermission(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	placeID string,
	accountIdentityID string,
	want bool,
) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(accountIdentityID)
	require.NoError(t, err)
	can, err := policyv1.MapPlace.Manage(placeID)
	require.NoError(t, err)
	allowed, err := spiceDB.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	require.Equal(t, want, allowed)
}

var _ domainaudit.Appender = failingMapPlaceAuditAppender{}
