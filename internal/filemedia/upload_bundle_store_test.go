package filemedia_test

import (
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/stretchr/testify/require"
)

func TestClientBundlePreparationPersistsOnceAndReusesSamePlan(t *testing.T) {
	for _, kind := range []string{"audio", "video"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			row, command, member := filemedia.ExportClientBundleFixture(t, kind)
			row.ClientMediaBundleID, row.ClientMediaManifest = nil, nil
			row.Status = domain.UploadSessionStatusUploading
			s := filemediatest.NewLibraryStore()
			s.UploadSessions[row.UploadID] = row
			service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
			ctx := auth.WithUser(t.Context(), &auth.UserInfo{MemberID: auth.MemberID(member), Authenticated: true, Onboarded: true})
			id, err := filemedia.ExportPrepareBundle(service, ctx, row, command)
			require.NoError(t, err)
			require.NotEmpty(t, id)
			stored := s.UploadSessions[row.UploadID]
			require.Equal(t, new(id), stored.ClientMediaBundleID)
			require.NotNil(t, stored.ClientMediaManifest)
			idAgain, err := filemedia.ExportPrepareBundle(service, ctx, stored, command)
			require.NoError(t, err)
			require.Equal(t, id, idAgain)
			require.Equal(t, stored, s.UploadSessions[row.UploadID])
			command.DurationSeconds++
			_, err = filemedia.ExportPrepareBundle(service, ctx, stored, command)
			require.Error(t, err)
			require.Equal(t, stored, s.UploadSessions[row.UploadID])
		})
	}
}

func TestClientBundlePreparationDoesNotClaimAfterStateChangeOrStoreFailure(t *testing.T) {
	for _, state := range []string{"finalizing", "failure"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			row, command, member := filemedia.ExportClientBundleFixture(t, "audio")
			row.ClientMediaBundleID, row.ClientMediaManifest = nil, nil
			row.Status = domain.UploadSessionStatusUploading
			stored := row
			s := filemediatest.NewLibraryStore()
			if state == "finalizing" {
				stored.Status = domain.UploadSessionStatusFinalizing
			} else {
				s.Fail = errors.New("bundle write failed")
			}
			s.UploadSessions[row.UploadID] = stored
			service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
			ctx := auth.WithUser(t.Context(), &auth.UserInfo{MemberID: auth.MemberID(member), Authenticated: true})
			id, err := filemedia.ExportPrepareBundle(service, ctx, row, command)
			require.Error(t, err)
			require.Empty(t, id)
			if state == "finalizing" {
				var outcome *domainerr.Error
				require.ErrorAs(t, err, &outcome)
				require.Equal(t, domainerr.FailedPrecondition, outcome.Kind)
			} else {
				require.ErrorIs(t, err, s.Fail)
			}
			require.Equal(t, stored, s.UploadSessions[row.UploadID])
		})
	}
}
