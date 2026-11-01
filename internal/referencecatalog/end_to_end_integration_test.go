//go:build integration

package referencecatalog_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	referencecatalogmenuadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog/menu"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	referencecatalogpostgres "github.com/echovisionlab/geul-api/internal/referencecatalog/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// These tests run the use cases on PostgreSQL, SpiceDB and the identity
// fence's Kratos rows together, for what neither the fakes nor the Store
// contract can show: the policy relationships SpiceDB derives permissions
// from and the roles behind them, the fence over real Identity rows, and
// authority rechecked after waiting on a real lock.

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

// endToEnd is the Reference Catalog use cases on one test's database and
// SpiceDB, over the production Store and adapters.
type endToEnd struct {
	stack *testutil.OryStack
	db    *gorm.DB
}

func newEndToEnd(t *testing.T) endToEnd {
	t.Helper()
	stack := testutil.PrepareOryIntegrationTest(t)
	require.NotNil(t, stack)
	return endToEnd{stack: stack, db: stack.DB}
}

func (e endToEnd) service(t *testing.T, audit domainaudit.Appender) *referencecatalog.Service {
	t.Helper()
	units, err := referencecatalogpostgres.NewUnits(e.db, referencecatalogpostgres.Ports{
		Assets:      referencecatalogadapter.NewAssets("https://cdn.example.com"),
		Members:     referencecatalogadapter.NewMemberSummaries("https://cdn.example.com"),
		MenuTargets: referencecatalogmenuadapter.NewTargets(audit),
		ClientWorks: workadapter.ClientWorks{},
	})
	require.NoError(t, err)
	service, err := referencecatalog.NewService(referencecatalog.Deps{
		Units: units, SpiceDB: e.stack.SpiceDBClient, Fence: identitystate.PostgresFence{}, Audit: audit,
	})
	require.NoError(t, err)
	return service
}

// user is a Kratos Identity and Member holding role, with a request context.
func (e endToEnd) user(t *testing.T, role policyv1.RoleID) (context.Context, *testutil.OryUser) {
	t.Helper()
	user := e.stack.CreateUser(t, role.ID())
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.77")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), user.AuthUserInfo()), user
}

func (e endToEnd) canManage(t *testing.T, manage func(string) (policyv1.Can, error), id, identityID string) bool {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	can, err := manage(id)
	require.NoError(t, err)
	allowed, err := e.stack.SpiceDBClient.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	return allowed
}

func (e endToEnd) rows(t *testing.T, table, id string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, e.db.Table(table).Where("id = ?", id).Count(&count).Error)
	return count
}

func TestReferenceCatalogPolicyRelationshipsFollowCommittedMutationsIntegration(t *testing.T) {
	t.Parallel()
	e := newEndToEnd(t)
	ctx, admin := e.user(t, policyv1.Role.Admin())
	service := e.service(t, &audittest.Recorder{})
	failing := e.service(t, &audittest.Recorder{Err: errors.New("audit unavailable")})

	type catalogRow struct {
		table  string
		manage func(string) (policyv1.Can, error)
		create func(*referencecatalog.Service) (string, error)
		delete func(*referencecatalog.Service, string) error
	}
	reference := func(kind referenceKind, manage func(string) (policyv1.Can, error)) catalogRow {
		return catalogRow{
			table: kind.resource(), manage: manage,
			create: func(s *referencecatalog.Service) (string, error) {
				created, err := kind.create(s, ctx, referencecatalog.CreateReferenceCommand{Name: "Policy " + uuid.NewString()[:8]})
				return created.ID, err
			},
			delete: func(s *referencecatalog.Service, id string) error {
				return kind.delete(s, ctx, referencecatalog.DeleteReferenceCommand{ID: id})
			},
		}
	}
	rows := []catalogRow{
		reference(referenceKinds[0], policyv1.Category.Manage),
		reference(referenceKinds[1], policyv1.Tag.Manage),
		reference(referenceKinds[2], policyv1.Genre.Manage),
		reference(referenceKinds[3], policyv1.Style.Manage),
		reference(referenceKinds[4], policyv1.Format.Manage),
		{
			table: "client", manage: policyv1.Client.Manage,
			create: func(s *referencecatalog.Service) (string, error) {
				created, err := s.CreateClient(ctx, referencecatalog.CreateClientCommand{Name: "Policy client " + uuid.NewString()[:8]})
				return created.Client.ID, err
			},
			delete: func(s *referencecatalog.Service, id string) error {
				return s.DeleteClient(ctx, referencecatalog.DeleteClientCommand{ID: id})
			},
		},
		{
			table: "map_place", manage: policyv1.MapPlace.Manage,
			create: func(s *referencecatalog.Service) (string, error) {
				created, err := s.CreateMapPlace(ctx, referencecatalog.CreateMapPlaceCommand{Name: "Policy place", Address: "Causal Road", Lat: 37.5, Lng: 127})
				return created.Place.ID, err
			},
			delete: func(s *referencecatalog.Service, id string) error {
				return s.DeleteMapPlace(ctx, referencecatalog.DeleteMapPlaceCommand{ID: id})
			},
		},
	}
	for _, row := range rows {
		id, err := row.create(service)
		require.NoError(t, err, row.table)
		require.True(t, e.canManage(t, row.manage, id, admin.IdentityID), "%s: the policy is written with the row", row.table)

		require.Error(t, row.delete(failing, id), row.table)
		require.True(t, e.canManage(t, row.manage, id, admin.IdentityID), "%s: a rolled back delete keeps the policy", row.table)
		require.EqualValues(t, 1, e.rows(t, row.table, id), row.table)

		require.NoError(t, row.delete(service, id), row.table)
		require.False(t, e.canManage(t, row.manage, id, admin.IdentityID), "%s: the policy is removed with the row", row.table)
		require.Zero(t, e.rows(t, row.table, id), row.table)
	}
}

func TestReferenceMutationRechecksTheAdministratorAfterWaitingOnTheIdentityLockIntegration(t *testing.T) {
	t.Parallel()
	e := newEndToEnd(t)
	ctx, admin := e.user(t, policyv1.Role.Admin())
	service := e.service(t, &audittest.Recorder{})
	created, err := service.CreateCategory(ctx, referencecatalog.CreateReferenceCommand{Name: "Recheck original"})
	require.NoError(t, err)

	roleChange := e.db.Begin()
	require.NoError(t, roleChange.Error)
	require.NoError(t, roleChange.Exec("SELECT id FROM kratos.identities WHERE id = ?::uuid FOR UPDATE", admin.IdentityID).Error)
	result := make(chan error, 1)
	go func() {
		name := "Recheck updated"
		_, err := service.UpdateCategory(ctx, referencecatalog.UpdateReferenceCommand{ID: created.ID, Name: &name})
		result <- err
	}()
	testutil.RequireKratosLockWaiters(t, e.db, 1)
	testutil.GrantIntegrationGlobalRole(t, e.stack.SpiceDBClient, admin.IdentityID, policyv1.Role.User())
	require.NoError(t, roleChange.Commit().Error)

	require.Equal(t, "admin access required", messageOf(<-result))
	var name string
	require.NoError(t, e.db.Table("category").Select("name").Where("id = ?", created.ID).Scan(&name).Error)
	require.Equal(t, "Recheck original", name)
}

func TestMapPlaceMutationsAuthorizeTheCurrentRoleAndIdentityIntegration(t *testing.T) {
	t.Parallel()
	e := newEndToEnd(t)
	// The request is stamped with the user's role at creation; each call
	// must use the role and Identity state current in its own transaction.
	ctx, user := e.user(t, policyv1.Role.Admin())
	service := e.service(t, &audittest.Recorder{})
	setRole := func(role policyv1.RoleID) {
		testutil.GrantIntegrationGlobalRole(t, e.stack.SpiceDBClient, user.IdentityID, role)
	}

	setRole(policyv1.Role.User())
	_, err := service.CreateMapPlace(ctx, referencecatalog.CreateMapPlaceCommand{Name: "Rejected", Address: "1 Authority Road"})
	require.Equal(t, "author access required", messageOf(err))

	setRole(policyv1.Role.Author())
	created, err := service.CreateMapPlace(ctx, referencecatalog.CreateMapPlaceCommand{Name: "Current authority place", Address: "2 Authority Road"})
	require.NoError(t, err)
	require.Equal(t, user.MemberID, created.CreatedByMember.ID)
	require.Equal(t, "admin access required", messageOf(service.DeleteMapPlace(ctx, referencecatalog.DeleteMapPlaceCommand{ID: created.Place.ID})))

	setRole(policyv1.Role.User())
	name := "Rejected update"
	_, err = service.UpdateMapPlace(ctx, referencecatalog.UpdateMapPlaceCommand{ID: created.Place.ID, Name: &name})
	require.Equal(t, "author access required", messageOf(err))

	setRole(policyv1.Role.Admin())
	require.NoError(t, e.db.Exec(`UPDATE kratos.identities SET state = 'inactive' WHERE id = ?::uuid`, user.IdentityID).Error)
	_, err = service.UpdateMapPlace(ctx, referencecatalog.UpdateMapPlaceCommand{ID: created.Place.ID, Name: &name})
	require.Equal(t, "author access required", messageOf(err), "an inactive Identity is fenced out")

	var stored string
	require.NoError(t, e.db.Table("map_place").Select("name").Where("id = ?", created.Place.ID).Scan(&stored).Error)
	require.Equal(t, "Current authority place", stored)
}
