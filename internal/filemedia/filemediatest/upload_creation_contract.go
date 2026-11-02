package filemediatest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func uploadCreationCases() map[string]func(*testing.T, UploadHarness) {
	cases := map[string]func(*testing.T, UploadHarness){
		"Track cleanup filters owner and preserves completion authority": func(t *testing.T, h UploadHarness) {
			track := uuid.NewString()
			ids := make([]string, 0, 5)
			for _, status := range []domain.UploadSessionStatus{domain.UploadSessionStatusInitiated, domain.UploadSessionStatusUploading, domain.UploadSessionStatusFinalizing, domain.UploadSessionStatusFailed, domain.UploadSessionStatusAborted} {
				row := contractUpload()
				row.UploadType, row.EntityID, row.EntityType, row.Status = "UPLOAD_TYPE_TRACK_AUDIO", track, new("TRANSCODE_ENTITY_TYPE_TRACK"), status
				ids = append(ids, row.UploadID)
				h.Seed(t, row, nil)
			}
			other := contractUpload()
			h.Seed(t, other, nil)
			now := other.CreatedAt.Add(time.Hour)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				rows, err := s.TrackUploadSessions(ctx, track)
				require.NoError(t, err)
				require.Len(t, rows, 5)
				var got []string
				for _, r := range rows {
					got = append(got, r.UploadID)
				}
				require.ElementsMatch(t, ids, got)
				exists, err := s.HasTrackUploadSessions(ctx, track)
				require.NoError(t, err)
				require.True(t, exists)
				exists, err = s.HasTrackUploadSessions(ctx, uuid.NewString())
				require.NoError(t, err)
				require.False(t, exists)
				count, err := s.AbortUploadSessions(ctx, append(ids, uuid.NewString()), now)
				require.NoError(t, err)
				require.Equal(t, int64(4), count)
				rows, err = s.TrackUploadSessions(ctx, track)
				require.NoError(t, err)
				for _, r := range rows {
					if r.UploadID == ids[2] {
						require.Equal(t, domain.UploadSessionStatusFinalizing, r.Status)
					} else {
						require.Equal(t, domain.UploadSessionStatusAborted, r.Status)
						require.True(t, now.Equal(r.LastActivityAt))
					}
				}
				gotOther, err := s.UploadSession(ctx, other.UploadID, other.FileID, false)
				require.NoError(t, err)
				require.Equal(t, domain.UploadSessionStatusInitiated, gotOther.Status)
				return nil
			}))
		},
		"Track session lock blocks concurrent abort": func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			row.UploadType, row.EntityID, row.EntityType = "UPLOAD_TYPE_TRACK_AUDIO", uuid.NewString(), new("TRANSCODE_ENTITY_TYPE_TRACK")
			h.Seed(t, row, nil)
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				rows, err := s.TrackUploadSessions(ctx, row.EntityID)
				require.Len(t, rows, 1)
				return err
			}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error {
				count, err := s.AbortUploadSessions(ctx, []string{row.UploadID}, time.Now())
				require.Equal(t, int64(1), count)
				return err
			}))
		},
		"new upload and cleanup rollback with owner": func(t *testing.T, h UploadHarness) {
			row, inserted := contractUpload(), contractUpload()
			h.Seed(t, row, nil)
			abort := errors.New("owner rollback")
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.InsertUploadSession(ctx, inserted, domain.UploadTargetGeneral)
				require.NoError(t, err)
				_, err = s.AbortUploadSessions(ctx, []string{row.UploadID}, row.CreatedAt.Add(time.Hour))
				require.NoError(t, err)
				return abort
			}), abort)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.UploadSession(ctx, inserted.UploadID, inserted.FileID, false)
				require.ErrorIs(t, err, domain.ErrUploadSessionNotFound)
				got, err := s.UploadSession(ctx, row.UploadID, row.FileID, false)
				require.NoError(t, err)
				require.Equal(t, row.Status, got.Status)
				require.True(t, row.LastActivityAt.Equal(got.LastActivityAt))
				return nil
			}))
		},
	}
	for _, mode := range []domain.UploadTargetMode{domain.UploadTargetGeneral, domain.UploadTargetEditorFile, domain.UploadTargetTrackProjection} {
		name := map[domain.UploadTargetMode]string{domain.UploadTargetGeneral: "general", domain.UploadTargetEditorFile: "editor", domain.UploadTargetTrackProjection: "track"}[mode]
		cases["session creation retains "+name+" target semantics"] = func(t *testing.T, h UploadHarness) {
			row := contractUpload()
			row.EntityID = uuid.NewString()
			if mode == domain.UploadTargetEditorFile {
				row.UploadType = "UPLOAD_TYPE_EDITOR_IMAGE"
				row.EntityType, row.SlotID, row.ExpectedFileID = new("TRANSCODE_ENTITY_TYPE_POST"), new(uuid.NewString()), new(uuid.NewString())
			}
			if mode == domain.UploadTargetTrackProjection {
				row.UploadType, row.EntityType, row.ExpectedFileID = "UPLOAD_TYPE_TRACK_AUDIO", new("TRANSCODE_ENTITY_TYPE_TRACK"), new(uuid.NewString())
			}
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				created, err := s.InsertUploadSession(ctx, row, mode)
				require.NoError(t, err)
				require.Equal(t, row.UploadID, created.UploadID)
				return nil
			}))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.UploadSession(ctx, row.UploadID, row.FileID, false)
				require.NoError(t, err)
				require.Equal(t, row.AttemptID, got.AttemptID)
				if mode == domain.UploadTargetTrackProjection {
					require.Equal(t, row.EntityID, got.EntityID)
					require.Equal(t, row.EntityType, got.EntityType)
					require.Equal(t, row.ExpectedFileID, got.ExpectedFileID)
				} else {
					require.Empty(t, got.EntityID)
					require.Nil(t, got.EntityType)
					require.Nil(t, got.SlotID)
					require.Nil(t, got.ExpectedFileID)
				}
				return nil
			}))
		}
	}
	return cases
}
