//go:build integration

package audience_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	audiencedomain "github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAudienceMutationsRecheckAdminAfterRootLockIntegration(t *testing.T) {
	t.Run("audience create", func(t *testing.T) {
		db, spiceDB, actorID := seedAudienceAuthorityActor(t)
		service := newAudienceServiceForTest(db, spiceDB)
		identityLock := db.Begin()
		require.NoError(t, identityLock.Error)
		t.Cleanup(func() { _ = identityLock.Rollback().Error })
		require.NoError(t, identityLock.Exec(
			"SELECT id FROM kratos.identities WHERE id = ?::uuid FOR UPDATE",
			actorID,
		).Error)
		result := make(chan error, 1)
		go func() {
			_, err := service.CreateSegment(
				audienceAuthorityContext(actorID),
				connect.NewRequest(&managev1.CreateSegmentRequest{
					Name: "revoked segment", SegmentType: managev1.SegmentType_SEGMENT_TYPE_ALL_MEMBERS,
				}),
			)
			result <- err
		}()
		requireAudienceMutationWaiting(t, result)
		grantIntegrationGlobalRole(t, spiceDB, actorID, policyv1.Role.User())
		require.NoError(t, identityLock.Commit().Error)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))
		var count int64
		require.NoError(t, db.Table("audience_segment").Where("name = ?", "revoked segment").Count(&count).Error)
		require.Zero(t, count)
	})

	for _, test := range []struct {
		name    string
		restore bool
		invoke  func(context.Context, *audiencedomain.AudienceService, string) error
		assert  func(*testing.T, *gorm.DB, string)
	}{
		{
			name: "audience update",
			invoke: func(ctx context.Context, service *audiencedomain.AudienceService, segmentID string) error {
				name := "must not update"
				_, err := service.UpdateSegment(ctx, connect.NewRequest(&managev1.UpdateSegmentRequest{Id: segmentID, Name: &name}))
				return err
			},
			assert: func(t *testing.T, db *gorm.DB, segmentID string) {
				var name string
				require.NoError(t, db.Table("audience_segment").Select("name").Where("id = ?", segmentID).Scan(&name).Error)
				require.Equal(t, "authority race", name)
			},
		},
		{
			name: "audience archive",
			invoke: func(ctx context.Context, service *audiencedomain.AudienceService, segmentID string) error {
				_, err := service.ArchiveSegment(ctx, connect.NewRequest(&managev1.ArchiveSegmentRequest{Id: segmentID}))
				return err
			},
			assert: func(t *testing.T, db *gorm.DB, segmentID string) {
				var segment model.AudienceSegment
				require.NoError(t, db.Select("archived_at").First(&segment, "id = ?", segmentID).Error)
				require.Nil(t, segment.ArchivedAt)
			},
		},
		{
			name: "audience restore", restore: true,
			invoke: func(ctx context.Context, service *audiencedomain.AudienceService, segmentID string) error {
				_, err := service.RestoreSegment(ctx, connect.NewRequest(&managev1.RestoreSegmentRequest{Id: segmentID}))
				return err
			},
			assert: func(t *testing.T, db *gorm.DB, segmentID string) {
				var segment model.AudienceSegment
				require.NoError(t, db.Select("archived_at").First(&segment, "id = ?", segmentID).Error)
				require.NotNil(t, segment.ArchivedAt)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, spiceDB, actorID := seedAudienceAuthorityActor(t)
			segmentID := seedAudienceAuthoritySegment(t, db, spiceDB, test.restore)
			service := newAudienceServiceForTest(db, spiceDB)

			rootLock := db.Begin()
			require.NoError(t, rootLock.Error)
			t.Cleanup(func() { _ = rootLock.Rollback().Error })
			require.NoError(t, rootLock.Exec(
				"SELECT id FROM audience_segment WHERE id = ?::uuid FOR UPDATE",
				segmentID,
			).Error)
			result := make(chan error, 1)
			go func() { result <- test.invoke(audienceAuthorityContext(actorID), service, segmentID) }()
			requireAudienceMutationWaiting(t, result)
			grantIntegrationGlobalRole(t, spiceDB, actorID, policyv1.Role.User())
			require.NoError(t, rootLock.Commit().Error)
			mutationErr := <-result
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(mutationErr), "error: %v", mutationErr)
			test.assert(t, db, segmentID)
		})
	}
}

func seedAudienceAuthorityActor(t *testing.T) (*gorm.DB, *auth.SpiceDBClient, string) {
	t.Helper()
	db := newAudienceConcurrentIntegrationDB(t)
	actorID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, actorID, "Audience authority race")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, actorID, policyv1.Role.Admin())
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM member WHERE id = ?::uuid", integrationMemberID(actorID)).Error
		_ = db.Exec("DELETE FROM account_identity WHERE id = ?::uuid", actorID).Error
		_ = db.Exec("DELETE FROM kratos.identities WHERE id = ?::uuid", actorID).Error
	})
	return db, spiceDB, actorID
}

func seedAudienceAuthoritySegment(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	archived bool,
) string {
	t.Helper()
	segmentID := integrationTestUUID()
	now := time.Now().UTC()
	var archivedAt any
	if archived {
		archivedAt = now
	}
	require.NoError(t, db.Exec(`
		INSERT INTO audience_segment (id, name, segment_type, archived_at, created_at, updated_at)
		VALUES (?::uuid, 'authority race', 'SEGMENT_TYPE_ALL_MEMBERS', ?, ?, ?)
	`, segmentID, archivedAt, now, now).Error)
	seedAudienceAccessSegmentPolicy(t, spiceDB, segmentID)
	t.Cleanup(func() { _ = db.Exec("DELETE FROM audience_segment WHERE id = ?::uuid", segmentID).Error })
	return segmentID
}

func audienceAuthorityContext(identityID string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		MemberID:      auth.MemberID(integrationMemberID(identityID)),
		SessionID:     auth.SessionID(integrationTestUUID()),
		Authenticated: true,
	})
}

func requireAudienceMutationWaiting(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		require.FailNow(t, "mutation returned before its root lock was released", "error: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
}
