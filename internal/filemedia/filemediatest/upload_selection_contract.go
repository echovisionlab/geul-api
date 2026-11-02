package filemediatest

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func uploadSelectionCases() map[string]func(*testing.T, UploadHarness) {
	cases := map[string]func(*testing.T, UploadHarness){
		"finalizing selection outlives idle window and chunk changes":         uploadSelectionContractFinalizingPriority,
		"Track selection binds entity and expected original":                  uploadSelectionContractTrackBinding,
		"dedicated slot selection preserves an empty candidate slot wildcard": uploadSelectionContractSlotWildcard,
		"candidate optional file metadata filters are conjunctive":            uploadSelectionContractCandidateMetadata,
	}
	for _, kind := range []string{"UPLOAD_TYPE_GENERAL_FILE", "UPLOAD_TYPE_EDITOR_IMAGE"} {
		cases["independent upload selection "+kind] = func(t *testing.T, h UploadHarness) {
			uploadSelectionContractIndependent(t, h, kind)
		}
	}
	return cases
}

func uploadSelectionContractFinalizingPriority(t *testing.T, h UploadHarness) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	owner := uuid.NewString()
	rows := []domain.UploadSession{}
	for _, state := range []string{"current", "stale", "chunk mismatch", "finalizing", "aborted"} {
		r := contractUpload()
		r.UploadType = "UPLOAD_TYPE_TRACK_AUDIO"
		r.EntityID = owner
		r.EntityType = new("TRANSCODE_ENTITY_TYPE_TRACK")
		r.LastActivityAt = now
		r.ChunkSize = 6
		switch state {
		case "stale":
			r.LastActivityAt = now.Add(-8 * 24 * time.Hour)
		case "chunk mismatch":
			r.ChunkSize = 9
		case "finalizing":
			r.Status = domain.UploadSessionStatusFinalizing
			r.LastActivityAt = now.Add(-8 * 24 * time.Hour)
			r.ChunkSize = 0
		case "aborted":
			r.Status = domain.UploadSessionStatusAborted
		}
		h.Seed(t, r, nil)
		rows = append(rows, r)
	}
	f := domain.UploadSearch{UploadType: "UPLOAD_TYPE_TRACK_AUDIO", EntityID: owner, EntityType: new("TRANSCODE_ENTITY_TYPE_TRACK"), Mode: domain.UploadTargetTrackProjection, ActiveAfter: now.Add(-7 * 24 * time.Hour), ChunkSize: 6}
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		got, err := s.ActiveSurfaceUploads(ctx, f)
		require.NoError(t, err)
		require.Len(t, got, 2)
		require.Equal(t, rows[3].UploadID, got[0].UploadID)
		require.Equal(t, rows[0].UploadID, got[1].UploadID)
		candidate, found, err := s.FindUploadCandidate(ctx, f)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, rows[3].UploadID, candidate.UploadID)
		return nil
	}))
}

func uploadSelectionContractTrackBinding(t *testing.T, h UploadHarness) {
	row := contractUpload()
	row.UploadType = "UPLOAD_TYPE_TRACK_AUDIO"
	row.EntityID = uuid.NewString()
	row.EntityType = new("TRANSCODE_ENTITY_TYPE_TRACK")
	row.ExpectedFileID = new(uuid.NewString())
	h.Seed(t, row, nil)
	f := domain.UploadSearch{UploadType: row.UploadType, EntityID: row.EntityID, EntityType: row.EntityType, Mode: domain.UploadTargetTrackProjection, ExpectedCurrentFileID: row.ExpectedFileID, ActiveAfter: row.LastActivityAt.Add(-time.Second), ChunkSize: row.ChunkSize}
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		got, found, err := s.FindUploadCandidate(ctx, f)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, row.UploadID, got.UploadID)
		for _, mismatch := range []string{"entity", "entity type", "expected original", "no expected original"} {
			bad := f
			switch mismatch {
			case "entity":
				bad.EntityID = uuid.NewString()
			case "entity type":
				bad.EntityType = new("TRANSCODE_ENTITY_TYPE_RELEASE")
			case "expected original":
				bad.ExpectedCurrentFileID = new(uuid.NewString())
			default:
				bad.ExpectedCurrentFileID = nil
			}
			_, found, err := s.FindUploadCandidate(ctx, bad)
			require.NoError(t, err)
			require.False(t, found)
			rows, err := s.ActiveSurfaceUploads(ctx, bad)
			require.NoError(t, err)
			require.Empty(t, rows)
		}
		return nil
	}))
}

func uploadSelectionContractSlotWildcard(t *testing.T, h UploadHarness) {
	a, b := contractUpload(), contractUpload()
	owner := uuid.NewString()
	for _, r := range []*domain.UploadSession{&a, &b} {
		r.UploadType = "UPLOAD_TYPE_FEATURED_IMAGE"
		r.EntityID = owner
		r.EntityType = new("TRANSCODE_ENTITY_TYPE_PAGE")
	}
	a.SlotID = new("first")
	b.SlotID = new("second")
	b.Status = domain.UploadSessionStatusFinalizing
	h.Seed(t, a, nil)
	h.Seed(t, b, nil)
	f := domain.UploadSearch{UploadType: a.UploadType, EntityID: owner, EntityType: a.EntityType, Mode: domain.UploadTargetGeneral, SlotID: "first", ActiveAfter: a.LastActivityAt.Add(-time.Second), ChunkSize: a.ChunkSize}
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		rows, err := s.ActiveSurfaceUploads(ctx, f)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, a.UploadID, rows[0].UploadID)
		f.SlotID = ""
		rows, err = s.ActiveSurfaceUploads(ctx, f)
		require.NoError(t, err)
		require.Nil(t, rows)
		r, found, err := s.FindUploadCandidate(ctx, f)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, b.UploadID, r.UploadID)
		return nil
	}))
}

func uploadSelectionContractCandidateMetadata(t *testing.T, h UploadHarness) {
	row := contractUpload()
	row.FileLastModified = new(int64(123))
	h.Seed(t, row, nil)
	f := domain.UploadSearch{UploadType: row.UploadType, Mode: domain.UploadTargetGeneral, ActiveAfter: row.LastActivityAt.Add(-time.Second), ChunkSize: row.ChunkSize, FileID: row.FileID, UploadID: row.UploadID, FileName: row.FileName, FileSize: &row.FileSize, MimeType: row.RequestedMime, FileLastModified: row.FileLastModified}
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		for _, mismatch := range []string{"none", "name", "size", "MIME", "modified", "zero size ignored"} {
			bad := f
			switch mismatch {
			case "name":
				bad.FileName = "other"
			case "size":
				bad.FileSize = new(int64(999))
			case "MIME":
				bad.MimeType = "image/png"
			case "modified":
				bad.FileLastModified = new(int64(456))
			case "zero size ignored":
				bad.FileSize = new(int64(0))
			}
			_, found, err := s.FindUploadCandidate(ctx, bad)
			require.NoError(t, err)
			require.Equal(t, mismatch == "none" || mismatch == "zero size ignored", found)
		}
		return nil
	}))
}

func uploadSelectionContractIndependent(t *testing.T, h UploadHarness, kind string) {
	row := contractUpload()
	row.UploadType = kind
	h.Seed(t, row, nil)
	mode := domain.UploadTargetGeneral
	if kind != "UPLOAD_TYPE_GENERAL_FILE" {
		mode = domain.UploadTargetEditorFile
	}
	f := domain.UploadSearch{UploadType: kind, Mode: mode, FileID: row.FileID, UploadID: row.UploadID, ActiveAfter: row.LastActivityAt.Add(-time.Second), ChunkSize: row.ChunkSize}
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		rows, err := s.ActiveSurfaceUploads(ctx, f)
		require.NoError(t, err)
		require.Nil(t, rows)
		got, found, err := s.FindUploadCandidate(ctx, f)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, row.UploadID, got.UploadID)
		f.UploadID = uuid.NewString()
		_, found, err = s.FindUploadCandidate(ctx, f)
		require.NoError(t, err)
		require.False(t, found)
		return nil
	}))
}
