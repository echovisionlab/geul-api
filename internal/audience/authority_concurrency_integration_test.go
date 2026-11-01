//go:build integration

package audience_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	audiencedomain "github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// An update that waited on a concurrent writer's segment lock checks the
// administrator's authority as SpiceDB answers once it proceeds, so a role
// revoked while it waited denies it.
func TestAudienceMutationRechecksAdminAfterWaitingOnTheRootLockIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.PrepareOryIntegrationConcurrentTest(t)
	db, spiceDB := stack.DB, stack.SpiceDBClient
	actorID := testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, actorID, "Audience authority race")
	testutil.GrantIntegrationGlobalRole(t, spiceDB, actorID, policyv1.Role.Admin())
	segmentID := testutil.IntegrationUUID()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO audience_segment (id, name, segment_type, created_at, updated_at)
		VALUES (?::uuid, 'authority race', 'SEGMENT_TYPE_ALL_MEMBERS', ?, ?)
	`, segmentID, now, now).Error)
	policy, err := policyv1.AudienceSegment.TouchPolicy(segmentID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)
	service := newIntegrationService(t, db, spiceDB, &audittest.Recorder{})

	rootLock := db.Begin()
	require.NoError(t, rootLock.Error)
	t.Cleanup(func() { _ = rootLock.Rollback().Error })
	require.NoError(t, rootLock.Exec("SELECT id FROM audience_segment WHERE id = ?::uuid FOR UPDATE", segmentID).Error)
	result := make(chan error, 1)
	go func() {
		ctx := auth.WithUser(context.Background(), &auth.UserInfo{
			IdentityID: auth.IdentityID(actorID), MemberID: auth.MemberID(memberID),
			SessionID: auth.SessionID(testutil.IntegrationUUID()), Authenticated: true,
		})
		name := "must not update"
		_, err := service.UpdateSegment(ctx, audiencedomain.UpdateSegmentCommand{ID: segmentID, Name: &name})
		result <- err
	}()
	testutil.RequireLockWaiters(t, db, 1)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, actorID, policyv1.Role.User())
	require.NoError(t, rootLock.Commit().Error)

	err = <-result
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code(), "error: %v", err)
	var name string
	require.NoError(t, db.Table("audience_segment").Select("name").Where("id = ?", segmentID).Scan(&name).Error)
	require.Equal(t, "authority race", name)
}
