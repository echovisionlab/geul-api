//go:build integration

package maptheme_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	mapthemepostgres "github.com/echovisionlab/geul-api/internal/maptheme/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// These tests run the use cases on PostgreSQL, SpiceDB and the identity
// fence's Kratos rows together, for what neither the fakes nor the Store
// contract can show: the policy relationships SpiceDB derives permissions
// from, and authority rechecked after waiting on a real lock.

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := orytest.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start map theme integration suite: %v\n", err)
		os.Exit(1)
	}
	orytest.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	orytest.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close map theme integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

// endToEnd is the Map Theme use cases on one test's database and SpiceDB,
// and an administrator seeded as Member and Identity rows.
type endToEnd struct {
	db       *gorm.DB
	spiceDB  *auth.SpiceDBClient
	admin    principal
	themes   *maptheme.ConnectHandler
	internal *maptheme.InternalConnectHandler
}

func newEndToEnd(t *testing.T) endToEnd {
	t.Helper()
	stack := orytest.PrepareOryIntegrationTest(t)
	require.NotNil(t, stack)
	identityID := orytest.IntegrationUUID()
	memberID := orytest.SeedPostIntegrationIdentity(t, stack.DB, identityID, "Map Theme administrator")
	orytest.GrantIntegrationGlobalRole(t, stack.SpiceDBClient, identityID, policyv1.Role.Admin())
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.121")
	require.NoError(t, err)
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(context.Background(), requestContext), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(orytest.IntegrationUUID()), Authenticated: true, Onboarded: true,
	})
	e := endToEnd{db: stack.DB, spiceDB: stack.SpiceDBClient, admin: principal{identityID: identityID, memberID: memberID, ctx: ctx}}
	e.themes, e.internal = e.handlers(t, &audittest.Recorder{})
	return e
}

func (e endToEnd) handlers(t *testing.T, audit domainaudit.Appender) (*maptheme.ConnectHandler, *maptheme.InternalConnectHandler) {
	t.Helper()
	service, err := maptheme.NewService(maptheme.Deps{
		Units: mapthemepostgres.NewUnits(e.db), SpiceDB: e.spiceDB, Fence: identitystate.PostgresFence{}, Audit: audit,
	})
	require.NoError(t, err)
	return maptheme.NewConnectHandler(service), maptheme.NewInternalConnectHandler(service)
}

func (e endToEnd) canManage(t *testing.T, themeID string) bool {
	t.Helper()
	can, err := policyv1.MapTheme.Manage(themeID)
	require.NoError(t, err)
	actor, err := policyv1.NewAccountIdentityActor(e.admin.identityID)
	require.NoError(t, err)
	allowed, err := e.spiceDB.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	return allowed
}

func (e endToEnd) rows(t *testing.T, where string, args ...any) int64 {
	t.Helper()
	var count int64
	require.NoError(t, e.db.Table("map_theme").Where(where, args...).Count(&count).Error)
	return count
}

func TestMapThemePolicyRelationshipsFollowCommittedMutationsIntegration(t *testing.T) {
	t.Parallel()
	e := newEndToEnd(t)

	created, err := e.themes.CreateMapTheme(e.admin.ctx, connect.NewRequest(createRequest("Synchronous map theme")))
	require.NoError(t, err)
	require.True(t, e.canManage(t, created.Msg.Id), "the policy is written with the theme")
	copied, err := e.themes.CopyMapTheme(e.admin.ctx, connect.NewRequest(&managev1.CopyMapThemeRequest{Id: created.Msg.Id, Name: "Synchronous map theme copy"}))
	require.NoError(t, err)
	require.True(t, e.canManage(t, copied.Msg.Id))
	_, err = e.themes.DeleteMapTheme(e.admin.ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: copied.Msg.Id}))
	require.NoError(t, err)
	require.False(t, e.canManage(t, copied.Msg.Id), "the policy is removed with the theme")

	failing, _ := e.handlers(t, &audittest.Recorder{Err: fmt.Errorf("audit unavailable")})
	_, err = failing.DeleteMapTheme(e.admin.ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: created.Msg.Id}))
	require.Error(t, err)
	require.True(t, e.canManage(t, created.Msg.Id), "a rolled back delete keeps the policy")
	require.EqualValues(t, 1, e.rows(t, "id = ?", created.Msg.Id))
}

// waitForLockWaiter waits until a transaction of db's database waits for a
// lock another holds; result fails the test when the mutation returned
// without waiting. The Kratos tables of a test database are postgres_fdw
// tables of the shared Kratos database, so a wait on an Identity row shows
// here as a backend waiting for the remote result, not in pg_locks.
func waitForLockWaiter(t *testing.T, db *gorm.DB, result <-chan error) {
	t.Helper()
	local := mutationtest.PostgresHarness[maptheme.Store](db, nil).Waiting
	waiting := func() int {
		var remote int
		require.NoError(t, db.Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event = 'PostgresFdwGetResult'`).Scan(&remote).Error)
		return local(t) + remote
	}
	deadline := time.Now().Add(10 * time.Second)
	for waiting() == 0 {
		select {
		case err := <-result:
			t.Fatalf("the mutation returned before waiting for the held lock: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the mutation never waited for the held lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMapThemeMutationsRecheckAuthorityAfterWaitingOnALockIntegration(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		// lock takes the row the mutation waits on.
		lock   func(tx *gorm.DB, e endToEnd, themeID string) error
		invoke func(e endToEnd, themeID string) error
		// unchanged asserts the denied mutation changed nothing.
		unchanged func(t *testing.T, e endToEnd, themeID string)
	}{
		{
			// The identity fence waits on the administrator's Identity.
			name: "create",
			lock: func(tx *gorm.DB, e endToEnd, _ string) error {
				return tx.Exec("SELECT id FROM kratos.identities WHERE id = ?::uuid FOR UPDATE", e.admin.identityID).Error
			},
			invoke: func(e endToEnd, _ string) error {
				_, err := e.themes.CreateMapTheme(e.admin.ctx, connect.NewRequest(createRequest("Role-revoked create")))
				return err
			},
			unchanged: func(t *testing.T, e endToEnd, _ string) {
				require.Zero(t, e.rows(t, "name = ?", "Role-revoked create"))
			},
		},
		{
			// The root lock comes before the fresh administrator check.
			name: "delete",
			lock: lockThemeRow,
			invoke: func(e endToEnd, themeID string) error {
				_, err := e.themes.DeleteMapTheme(e.admin.ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: themeID}))
				return err
			},
			unchanged: func(t *testing.T, e endToEnd, themeID string) {
				require.EqualValues(t, 1, e.rows(t, "id = ?", themeID))
			},
		},
		{
			// The root lock comes before the contributor is re-resolved and
			// checked as an actor.
			name: "collaboration save",
			lock: lockThemeRow,
			invoke: func(e endToEnd, themeID string) error {
				_, err := e.internal.SaveMapThemeSnapshot(context.Background(), connect.NewRequest(collaborationSave(themeID, 1, e.admin.memberID, "Role-revoked update")))
				return err
			},
			unchanged: func(t *testing.T, e endToEnd, themeID string) {
				require.EqualValues(t, 1, e.rows(t, "id = ? AND edit_version = 1 AND name <> ?", themeID, "Role-revoked update"))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			e := newEndToEnd(t)
			theme, err := e.themes.CreateMapTheme(e.admin.ctx, connect.NewRequest(createRequest("Role-revoked source")))
			require.NoError(t, err)

			lockTx := e.db.Begin()
			require.NoError(t, lockTx.Error)
			require.NoError(t, test.lock(lockTx, e, theme.Msg.Id))
			result := make(chan error, 1)
			go func() { result <- test.invoke(e, theme.Msg.Id) }()
			waitForLockWaiter(t, e.db, result)
			orytest.GrantIntegrationGlobalRole(t, e.spiceDB, e.admin.identityID, policyv1.Role.User())
			require.NoError(t, lockTx.Commit().Error)

			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))
			test.unchanged(t, e, theme.Msg.Id)
		})
	}
}

func lockThemeRow(tx *gorm.DB, _ endToEnd, themeID string) error {
	return tx.Exec("SELECT id FROM map_theme WHERE id = ?::uuid FOR UPDATE", themeID).Error
}
