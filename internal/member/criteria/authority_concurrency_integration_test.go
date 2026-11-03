//go:build integration

package criteria_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	// An update that waited on a concurrent writer's criteria lock checks the
	// administrator's authority as SpiceDB answers once it proceeds, so a role
	// revoked while it waited denies it.
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
)

func TestCriteriaMutationRechecksAdminAfterWaitingOnTheRootLockIntegration(t *testing.T) {
	t.Parallel()
	stack := orytest.PrepareOryIntegrationConcurrentTest(t)
	db, spiceDB := stack.DB, stack.SpiceDBClient
	actorID := orytest.IntegrationUUID()
	memberID := orytest.SeedPostIntegrationIdentity(t, db, actorID, "Criteria authority race")
	orytest.GrantIntegrationGlobalRole(t, spiceDB, actorID, policyv1.Role.Admin())
	criteriaID := orytest.IntegrationUUID()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO member_criteria (id, name, kind, created_at, updated_at)
		VALUES (?::uuid, 'authority race', 'MEMBER_CRITERIA_KIND_ALL_MEMBERS', ?, ?)
	`, criteriaID, now, now).Error)
	policy, err := policyv1.MemberCriteria.TouchPolicy(criteriaID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)
	service := newIntegrationService(t, db, spiceDB, &audittest.Recorder{})

	rootLock := db.Begin()
	require.NoError(t, rootLock.Error)
	t.Cleanup(func() { _ = rootLock.Rollback().Error })
	require.NoError(t, rootLock.Exec("SELECT id FROM member_criteria WHERE id = ?::uuid FOR UPDATE", criteriaID).Error)
	result := make(chan error, 1)
	go func() {
		ctx := auth.WithUser(context.Background(), &auth.UserInfo{
			IdentityID: auth.IdentityID(actorID), MemberID: auth.MemberID(memberID),
			SessionID: auth.SessionID(orytest.IntegrationUUID()), Authenticated: true,
		})
		name := "must not update"
		_, err := service.UpdateCriteria(ctx, membercriteria.UpdateCriteriaCommand{ID: criteriaID, Name: &name})
		result <- err
	}()
	pgtest.RequireLockWaiters(t, db, 1)
	orytest.GrantIntegrationGlobalRole(t, spiceDB, actorID, policyv1.Role.User())
	require.NoError(t, rootLock.Commit().Error)

	err = <-result
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code(), "error: %v", err)
	var name string
	require.NoError(t, db.Table("member_criteria").Select("name").Where("id = ?", criteriaID).Scan(&name).Error)
	require.Equal(t, "authority race", name)
}
