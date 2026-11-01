package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/application"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type deletionUnits struct {
	*mutationtest.UnitOfWork[application.DeletionStore]
}

func (u deletionUnits) Compensated(ctx context.Context, work func(application.DeletionStore, application.RegisterCompensation) error) error {
	return u.Transaction(ctx, true, func(tx mutation.Tx[application.DeletionStore]) error {
		var restore func(context.Context) error
		err := work(tx.Store(), func(at time.Time, compensate func(context.Context) error) error {
			if compensate == nil || at.IsZero() || restore != nil {
				return errors.New("invalid compensation")
			}
			restore = compensate
			return nil
		})
		if err != nil && restore != nil {
			return errors.Join(err, restore(context.WithoutCancel(ctx)))
		}
		return err
	})
}

type deletionAuthorization struct {
	calls         int
	restores      int
	fail          error
	beforeRestore func()
}

func (a *deletionAuthorization) DeleteAndVerify(context.Context, policyv1.Resource) (func(context.Context) error, time.Time, error) {
	a.calls++
	if a.fail != nil {
		return nil, time.Time{}, a.fail
	}
	return func(context.Context) error {
		a.restores++
		if a.beforeRestore != nil {
			a.beforeRestore()
		}
		return nil
	}, time.Now(), nil
}
func deletionFixture(t *testing.T) (*filemediatest.DeletionStore, deletionUnits, *deletionAuthorization, *application.Deletion, *filemedia.DeletionTarget) {
	t.Helper()
	s := filemediatest.NewDeletionStore()
	id := uuid.NewString()
	now := time.Now()
	s.Files[id] = filemedia.File{ID: id, Extension: "webp", MimeType: "image/webp", DeleteRequestedAt: &now}
	target, err := filemedia.CanonicalObjectTarget(s.Files[id])
	require.NoError(t, err)
	units := deletionUnits{mutationtest.New[application.DeletionStore](s)}
	authorization := &deletionAuthorization{}
	return s, units, authorization, application.NewDeletion(units, authorization), &filemedia.DeletionTarget{FileID: id, Original: target}
}
func TestDeletionRejectsChangedTargetsBeforeAuthorization(t *testing.T) {
	tests := map[string]func(*filemediatest.DeletionStore, *filemedia.DeletionTarget){
		"no request": func(s *filemediatest.DeletionStore, e *filemedia.DeletionTarget) {
			f := s.Files[e.FileID]
			f.DeleteRequestedAt = nil
			s.Files[e.FileID] = f
		},
		"original omitted": func(_ *filemediatest.DeletionStore, e *filemedia.DeletionTarget) { e.Original = nil },
		"file mismatch": func(_ *filemediatest.DeletionStore, e *filemedia.DeletionTarget) {
			e.Original.FileID = uuid.NewString()
		},
		"object key mismatch": func(_ *filemediatest.DeletionStore, e *filemedia.DeletionTarget) { e.Original.ObjectKey = "other" },
		"extension mismatch":  func(_ *filemediatest.DeletionStore, e *filemedia.DeletionTarget) { e.Original.Extension = "png" },
		"mime mismatch":       func(_ *filemediatest.DeletionStore, e *filemedia.DeletionTarget) { e.Original.MimeType = "image/png" },
		"referenced file": func(s *filemediatest.DeletionStore, e *filemedia.DeletionTarget) {
			s.References[e.FileID] = []string{"post.featured_image_file_id"}
		},
		"public asset ownership": func(_ *filemediatest.DeletionStore, e *filemedia.DeletionTarget) { e.HasPublicAssets = true },
		"pending transcode": func(s *filemediatest.DeletionStore, e *filemedia.DeletionTarget) {
			s.Transcodes[e.FileID] = [2]int64{1, 0}
		},
		"pending waveform": func(s *filemediatest.DeletionStore, e *filemedia.DeletionTarget) {
			s.Transcodes[e.FileID] = [2]int64{0, 1}
		},
		"extra generation": func(_ *filemediatest.DeletionStore, e *filemedia.DeletionTarget) {
			e.Generations = []*filemedia.GenerationTarget{{GenerationID: uuid.NewString()}}
		},
		"missing generation": func(s *filemediatest.DeletionStore, e *filemedia.DeletionTarget) {
			s.Generations[e.FileID] = []mediaasset.Generation{{ID: uuid.NewString(), FileID: e.FileID, Kind: "hls"}}
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _, a, h, event := deletionFixture(t)
			change(s, event)
			exists, err := h.ValidatePending(t.Context(), event)
			require.Error(t, err)
			require.False(t, exists)
			require.Error(t, h.Finalize(t.Context(), event))
			require.Zero(t, a.calls)
			require.Contains(t, s.Files, event.FileID)
		})
	}
}
func TestDeletionRequiresExactGenerationTargets(t *testing.T) {
	for _, kind := range []string{"matching", "duplicate", "nil", "prefix", "owner", "kind"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, _, a, h, e := deletionFixture(t)
			id := uuid.NewString()
			row := mediaasset.Generation{ID: id, FileID: e.FileID, Kind: "hls", ObjectPrefix: "media/hls/"}
			e.Generations = []*filemedia.GenerationTarget{{GenerationID: id, FileID: e.FileID, ObjectPrefix: row.ObjectPrefix}}
			switch kind {
			case "duplicate":
				s.Generations[e.FileID] = []mediaasset.Generation{row, {ID: uuid.NewString(), FileID: e.FileID, Kind: "hls"}}
				e.Generations = append(e.Generations, e.Generations[0])
			case "nil":
				e.Generations[0] = nil
			case "prefix":
				e.Generations[0].ObjectPrefix = "wrong"
			case "owner":
				row.FileID = uuid.NewString()
			case "kind":
				row.Kind = "other"
			}
			if kind != "duplicate" {
				s.Generations[e.FileID] = []mediaasset.Generation{row}
			}
			err := h.Finalize(t.Context(), e)
			if kind == "matching" {
				require.NoError(t, err)
				require.Equal(t, 1, a.calls)
				require.NotContains(t, s.Files, e.FileID)
			} else {
				require.ErrorIs(t, err, application.ErrInvalidFileDeleteTarget)
				require.Zero(t, a.calls)
				require.Contains(t, s.Files, e.FileID)
			}
		})
	}
}
func TestDeletionMissingFileIsIdempotent(t *testing.T) {
	s, _, a, h, e := deletionFixture(t)
	delete(s.Files, e.FileID)
	exists, err := h.ValidatePending(t.Context(), e)
	require.NoError(t, err)
	require.False(t, exists)
	require.NoError(t, h.Finalize(t.Context(), e))
	require.Zero(t, a.calls)
}
func TestDeletionPendingMeshBlocksFinalization(t *testing.T) {
	s, _, a, h, e := deletionFixture(t)
	s.MeshResults[e.FileID] = 1
	exists, err := h.ValidatePending(t.Context(), e)
	require.NoError(t, err)
	require.True(t, exists)
	require.ErrorContains(t, h.Finalize(t.Context(), e), "pending mesh")
	require.Zero(t, a.calls)
	require.Contains(t, s.Files, e.FileID)
}
func TestDeletionRestoresAuthorizationBeforeRollback(t *testing.T) {
	s, u, a, h, e := deletionFixture(t)
	failure := errors.New("delete failed")
	s.BeforeDelete = func() error { delete(s.Files, e.FileID); return failure }
	a.beforeRestore = func() { require.NotContains(t, s.Files, e.FileID) }
	require.ErrorIs(t, h.Finalize(t.Context(), e), failure)
	require.Equal(t, 1, a.calls)
	require.Equal(t, 1, a.restores)
	require.Contains(t, s.Files, e.FileID)
	require.Equal(t, []mutation.Step{mutation.StepAuthorizationLock, mutation.StepRollback}, u.Steps())
}
func TestDeletionAuthorizationFailurePreservesFile(t *testing.T) {
	s, _, a, h, e := deletionFixture(t)
	a.fail = errors.New("authorization failed")
	require.ErrorIs(t, h.Finalize(t.Context(), e), a.fail)
	require.Contains(t, s.Files, e.FileID)
	require.Zero(t, a.restores)
}
func TestDeletionSuccessCommitsWithoutCompensation(t *testing.T) {
	s, u, a, h, e := deletionFixture(t)
	require.NoError(t, h.Finalize(t.Context(), e))
	require.NotContains(t, s.Files, e.FileID)
	require.Equal(t, 1, a.calls)
	require.Zero(t, a.restores)
	require.Equal(t, []mutation.Step{mutation.StepAuthorizationLock, mutation.StepCommit}, u.Steps())
}
