package filemedia_test

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestFileRenameDecisionsUseStore(t *testing.T) {
	for name, tc := range map[string]struct {
		requested                       string
		missing, pending, changed, fail bool
	}{"rename": {requested: "renamed", changed: true}, "no change": {requested: "original"}, "missing": {requested: "renamed", missing: true, fail: true}, "pending deletion": {requested: "renamed", pending: true, fail: true}, "extension included": {requested: "renamed.WEBP", fail: true}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			row := filemediatest.CatalogFile("original", nil)
			if !tc.missing {
				s.Files[row.ID] = filemediatest.CatalogFileRecord{Row: row, PendingDeletion: tc.pending}
			}
			h := mutationtest.FakeHarness[filemedia.Store](s)
			var changed bool
			err := mutationtest.Transact(t, h, func(ctx context.Context, store filemedia.Store) error {
				var err error
				changed, err = filemedia.ExportRenameLockedFile(ctx, store, row.ID, tc.requested)
				return err
			})
			if tc.fail {
				require.Error(t, err)
				require.False(t, changed)
				require.Zero(t, s.Writes)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.changed, changed)
				require.Equal(t, tc.requested, s.Files[row.ID].Row.Name)
				if tc.changed {
					require.Equal(t, 1, s.Writes)
				} else {
					require.Zero(t, s.Writes)
				}
			}
		})
	}
}
func TestFileMovesRollbackAnEarlierMoveWhenAnotherFileIsPending(t *testing.T) {
	s := filemediatest.NewLibraryStore()
	a, b := filemediatest.CatalogFile("a", nil), filemediatest.CatalogFile("b", nil)
	a.ID = "11111111-1111-4111-8111-111111111111"
	b.ID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	s.Files[a.ID] = filemediatest.CatalogFileRecord{Row: a}
	s.Files[b.ID] = filemediatest.CatalogFileRecord{Row: b, PendingDeletion: true}
	h := mutationtest.FakeHarness[filemedia.Store](s)
	parent := uuid.NewString()
	err := mutationtest.Transact(t, h, func(ctx context.Context, store filemedia.Store) error {
		_, err := filemedia.ExportMoveLockedFiles(ctx, store, []string{a.ID, b.ID}, &parent)
		return err
	})
	require.Error(t, err)
	require.Nil(t, s.Files[a.ID].Row.ParentID)
	require.Nil(t, s.Files[b.ID].Row.ParentID)
	require.Zero(t, s.Writes)
}
func TestFileMovesSkipCurrentParentAndRejectMissingFiles(t *testing.T) {
	s := filemediatest.NewLibraryStore()
	parent := uuid.NewString()
	row := filemediatest.CatalogFile("same", &parent)
	s.Files[row.ID] = filemediatest.CatalogFileRecord{Row: row}
	h := mutationtest.FakeHarness[filemedia.Store](s)
	require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store filemedia.Store) error {
		moves, err := filemedia.ExportMoveLockedFiles(ctx, store, []string{row.ID}, &parent)
		require.Empty(t, moves)
		return err
	}))
	require.Zero(t, s.Writes)
	require.Error(t, mutationtest.Transact(t, h, func(ctx context.Context, store filemedia.Store) error {
		_, err := filemedia.ExportMoveLockedFiles(ctx, store, []string{row.ID, uuid.NewString()}, nil)
		return err
	}))
	require.Equal(t, &parent, s.Files[row.ID].Row.ParentID)
	require.Zero(t, s.Writes)
}
