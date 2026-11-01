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

func RunLibraryContract(t *testing.T, open func(*testing.T) mutationtest.Harness[domain.Store]) {
	t.Helper()
	cases := map[string]func(*testing.T, mutationtest.Harness[domain.Store]){
		"folder round trip": func(t *testing.T, h mutationtest.Harness[domain.Store]) {
			parent := domain.FileFolder{ID: uuid.NewString(), Name: "parent"}
			folder := domain.FileFolder{ID: uuid.NewString(), ParentID: &parent.ID, Name: "child"}
			require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, s domain.Store) error {
				_, err := s.InsertFolder(ctx, parent)
				require.NoError(t, err)
				got, err := s.InsertFolder(ctx, folder)
				require.NoError(t, err)
				folder = got
				require.False(t, got.CreatedAt.IsZero())
				require.False(t, got.UpdatedAt.IsZero())
				return nil
			}))
			require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, s domain.Store) error {
				got, err := s.Folder(ctx, folder.ID)
				require.NoError(t, err)
				require.True(t, folder.CreatedAt.Equal(got.CreatedAt))
				require.True(t, folder.UpdatedAt.Equal(got.UpdatedAt))
				require.Equal(t, folder.ParentID, got.ParentID)
				require.Equal(t, folder.Name, got.Name)
				exists, err := s.FolderExists(ctx, folder.ID)
				require.True(t, exists)
				return err
			}))
		},
		"missing folder sentinel": func(t *testing.T, h mutationtest.Harness[domain.Store]) {
			id := uuid.NewString()
			require.ErrorIs(t, mutationtest.Read(t, h, func(ctx context.Context, s domain.Store) error { _, err := s.Folder(ctx, id); return err }), domain.ErrFolderNotFound)
			require.ErrorIs(t, mutationtest.Transact(t, h, func(ctx context.Context, s domain.Store) error { _, err := s.LockFolder(ctx, id); return err }), domain.ErrFolderNotFound)
			require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, s domain.Store) error {
				exists, err := s.FolderExists(ctx, id)
				require.False(t, exists)
				return err
			}))
		},
		"root names are unique": func(t *testing.T, h mutationtest.Harness[domain.Store]) {
			insertFolder(t, h, nil, "name")
			require.ErrorIs(t, mutationtest.Transact(t, h, func(ctx context.Context, s domain.Store) error {
				_, err := s.InsertFolder(ctx, domain.FileFolder{ID: uuid.NewString(), Name: "name"})
				return err
			}), domain.ErrFolderNameExists)
		},
		"names are scoped to parent": func(t *testing.T, h mutationtest.Harness[domain.Store]) {
			parent := insertFolder(t, h, nil, "parent")
			insertFolder(t, h, nil, "shared")
			insertFolder(t, h, &parent.ID, "shared")
			require.ErrorIs(t, mutationtest.Transact(t, h, func(ctx context.Context, s domain.Store) error {
				_, err := s.InsertFolder(ctx, domain.FileFolder{ID: uuid.NewString(), ParentID: &parent.ID, Name: "shared"})
				return err
			}), domain.ErrFolderNameExists)
		},
		"rename and move persist": func(t *testing.T, h mutationtest.Harness[domain.Store]) {
			parent := insertFolder(t, h, nil, "parent")
			folder := insertFolder(t, h, nil, "initial")
			require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, s domain.Store) error {
				require.NoError(t, s.RenameFolder(ctx, folder.ID, "renamed"))
				require.NoError(t, s.MoveFolder(ctx, folder.ID, &parent.ID))
				row, err := s.Folder(ctx, folder.ID)
				require.NoError(t, err)
				require.Equal(t, "renamed", row.Name)
				require.Equal(t, &parent.ID, row.ParentID)
				return s.MoveFolder(ctx, folder.ID, nil)
			}))
			require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, s domain.Store) error {
				row, err := s.Folder(ctx, folder.ID)
				require.Nil(t, row.ParentID)
				return err
			}))
		},
		"rename collision sentinel": func(t *testing.T, h mutationtest.Harness[domain.Store]) {
			insertFolder(t, h, nil, "taken")
			folder := insertFolder(t, h, nil, "other")
			require.ErrorIs(t, mutationtest.Transact(t, h, func(ctx context.Context, s domain.Store) error { return s.RenameFolder(ctx, folder.ID, "taken") }), domain.ErrFolderNameExists)
		},
		"move collision sentinel": func(t *testing.T, h mutationtest.Harness[domain.Store]) {
			parent := insertFolder(t, h, nil, "parent")
			insertFolder(t, h, &parent.ID, "same")
			folder := insertFolder(t, h, nil, "same")
			require.ErrorIs(t, mutationtest.Transact(t, h, func(ctx context.Context, s domain.Store) error { return s.MoveFolder(ctx, folder.ID, &parent.ID) }), domain.ErrFolderNameExists)
		},
		"cycle detection follows ancestor chain": func(t *testing.T, h mutationtest.Harness[domain.Store]) {
			root := insertFolder(t, h, nil, "root")
			child := insertFolder(t, h, &root.ID, "child")
			leaf := insertFolder(t, h, &child.ID, "leaf")
			other := insertFolder(t, h, nil, "other")
			require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, s domain.Store) error {
				for _, pair := range [][2]string{{leaf.ID, root.ID}, {leaf.ID, child.ID}, {child.ID, child.ID}} {
					cycle, err := s.FolderMoveCreatesCycle(ctx, pair[0], pair[1])
					require.NoError(t, err)
					require.True(t, cycle)
				}
				cycle, err := s.FolderMoveCreatesCycle(ctx, other.ID, root.ID)
				require.False(t, cycle)
				return err
			}))
		},
		"rollback removes inserts": func(t *testing.T, h mutationtest.Harness[domain.Store]) {
			folder := domain.FileFolder{ID: uuid.NewString(), Name: "rollback"}
			abort := errors.New("abort")
			require.ErrorIs(t, mutationtest.Transact(t, h, func(ctx context.Context, s domain.Store) error {
				_, err := s.InsertFolder(ctx, folder)
				require.NoError(t, err)
				return abort
			}), abort)
			require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, s domain.Store) error {
				exists, err := s.FolderExists(ctx, folder.ID)
				require.False(t, exists)
				return err
			}))
		},
		"row lock serializes rename": func(t *testing.T, h mutationtest.Harness[domain.Store]) {
			folder := insertFolder(t, h, nil, "before")
			require.NoError(t, mutationtest.RequireSecondWaits(t, h, func(ctx context.Context, s domain.Store) error { _, err := s.LockFolder(ctx, folder.ID); return err }, func(ctx context.Context, s domain.Store) error { return s.RenameFolder(ctx, folder.ID, "after") }, func(ctx context.Context, s domain.Store) error {
				row, err := s.LockFolder(ctx, folder.ID)
				require.Equal(t, "after", row.Name)
				return err
			}))
		},
		"hierarchy lock serializes writers": func(t *testing.T, h mutationtest.Harness[domain.Store]) {
			folder := domain.FileFolder{ID: uuid.NewString(), Name: "after lock"}
			require.NoError(t, mutationtest.RequireSecondWaits(t, h, func(ctx context.Context, s domain.Store) error { return s.LockFolderHierarchy(ctx) }, func(ctx context.Context, s domain.Store) error { _, err := s.InsertFolder(ctx, folder); return err }, func(ctx context.Context, s domain.Store) error {
				if err := s.LockFolderHierarchy(ctx); err != nil {
					return err
				}
				exists, err := s.FolderExists(ctx, folder.ID)
				require.True(t, exists)
				return err
			}))
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) { t.Parallel(); run(t, open(t)) })
	}
}
func insertFolder(t *testing.T, h mutationtest.Harness[domain.Store], parent *string, name string) domain.FileFolder {
	t.Helper()
	folder := domain.FileFolder{ID: uuid.NewString(), ParentID: parent, Name: name, CreatedAt: time.Now().UTC().Truncate(time.Microsecond), UpdatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, s domain.Store) error {
		var err error
		folder, err = s.InsertFolder(ctx, folder)
		return err
	}))
	return folder
}
