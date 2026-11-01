//go:build integration

package release_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/model"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/releasetest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Upload cleanup runs between the authority check that guards it and the
// deletion transaction. A role revoked during cleanup is observed by the
// deletion itself, which rechecks the delete permission under its locks, so
// nothing is deleted on behalf of a caller that lost the authority.
func TestDeleteRechecksAuthorityRevokedDuringUploadCleanupIntegration(t *testing.T) {
	t.Parallel()

	t.Run("release", func(t *testing.T) {
		t.Parallel()
		fixture := newReleaseDeleteFixture(t, 1)
		fixture.seedUploadSession(t, fixture.trackIDs[0])
		rows, relationships, audits := fixture.rows(t), fixture.relationships(t), fixture.auditActions(t)
		fixture.cleaner.cleanup = func(trackID string) error {
			testutil.GrantIntegrationGlobalRole(t, fixture.spiceDB, fixture.adminID, policyv1.Role.User())
			return fixture.deleteUploadSessions(trackID)
		}

		response, err := fixture.delete()
		require.Nil(t, response)
		requireConnectError(t, err, connect.CodeNotFound, "release not found: "+fixture.releaseID)
		calls, _ := fixture.cleaner.recorded()
		require.Equal(t, fixture.trackIDs, calls)
		fixture.requireUntouched(t, rows, relationships, audits)
	})

	t.Run("track", func(t *testing.T) {
		t.Parallel()
		fixture := newReleaseDeleteFixture(t, 1)
		fixture.seedUploadSession(t, fixture.trackIDs[0])
		tracks := releasepkg.NewTrackConnectHandler(releasetest.NewService(t, releasetest.Deps{
			DB: fixture.db, SpiceDB: fixture.spiceDB, Audit: &audittest.Recorder{}, Files: fixture.cleaner,
		}))
		fixture.cleaner.cleanup = func(trackID string) error {
			testutil.GrantIntegrationGlobalRole(t, fixture.spiceDB, fixture.adminID, policyv1.Role.User())
			return fixture.deleteUploadSessions(trackID)
		}

		response, err := tracks.DeleteTrack(fixture.ctx, connect.NewRequest(&managev1.DeleteTrackRequest{Id: fixture.trackIDs[0]}))
		require.Nil(t, response)
		require.Error(t, err)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		var remaining int64
		require.NoError(t, fixture.db.Model(&model.Track{}).Where("id = ?", fixture.trackIDs[0]).Count(&remaining).Error)
		require.EqualValues(t, 1, remaining)
	})
}
