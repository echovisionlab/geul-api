package filemediatest

import (
	"context"
	"maps"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

type LibraryStore struct {
	Folders map[string]domain.FileFolder
	Files   map[string]CatalogFileRecord
	Fail    error
	Writes  int
}

func NewLibraryStore() *LibraryStore {
	return &LibraryStore{Files: map[string]CatalogFileRecord{}, Folders: map[string]domain.FileFolder{}}
}
func (s *LibraryStore) Savepoint() func() {
	saved := maps.Clone(s.Folders)
	files := maps.Clone(s.Files)
	writes := s.Writes
	return func() { s.Folders = saved; s.Files = files; s.Writes = writes }
}
func (s *LibraryStore) FolderExists(_ context.Context, id string) (bool, error) {
	_, exists := s.Folders[id]
	return exists, s.Fail
}
func (s *LibraryStore) Folder(_ context.Context, id string) (domain.FileFolder, error) {
	if s.Fail != nil {
		return domain.FileFolder{}, s.Fail
	}
	row, ok := s.Folders[id]
	if !ok {
		return domain.FileFolder{}, domain.ErrFolderNotFound
	}
	return row, nil
}
func (s *LibraryStore) LockFolder(ctx context.Context, id string) (domain.FileFolder, error) {
	return s.Folder(ctx, id)
}
func (s *LibraryStore) LockFolderHierarchy(context.Context) error { return s.Fail }
func (s *LibraryStore) InsertFolder(_ context.Context, row domain.FileFolder) (domain.FileFolder, error) {
	if s.Fail != nil {
		return domain.FileFolder{}, s.Fail
	}
	if s.duplicate(row) {
		return domain.FileFolder{}, domain.ErrFolderNameExists
	}
	now := time.Now().UTC()
	if row.CreatedAt.IsZero() {
		row.CreatedAt = now
	}
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = now
	}
	s.Folders[row.ID] = row
	s.Writes++
	return row, nil
}
func (s *LibraryStore) duplicate(row domain.FileFolder) bool {
	for id, value := range s.Folders {
		if id != row.ID && value.Name == row.Name && sameParent(value.ParentID, row.ParentID) {
			return true
		}
	}
	return false
}
func sameParent(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func (s *LibraryStore) FolderMoveCreatesCycle(_ context.Context, parent, id string) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	seen := map[string]bool{}
	for parent != "" && !seen[parent] {
		if parent == id {
			return true, nil
		}
		seen[parent] = true
		row, ok := s.Folders[parent]
		if !ok || row.ParentID == nil {
			break
		}
		parent = *row.ParentID
	}
	return false, nil
}
func (s *LibraryStore) RenameFolder(_ context.Context, id, name string) error {
	if s.Fail != nil {
		return s.Fail
	}
	row, ok := s.Folders[id]
	if !ok {
		return nil
	}
	row.Name = name
	if s.duplicate(row) {
		return domain.ErrFolderNameExists
	}
	row.UpdatedAt = time.Now().UTC()
	s.Folders[id] = row
	s.Writes++
	return nil
}
func (s *LibraryStore) MoveFolder(_ context.Context, id string, parent *string) error {
	if s.Fail != nil {
		return s.Fail
	}
	row, ok := s.Folders[id]
	if !ok {
		return nil
	}
	row.ParentID = parent
	if s.duplicate(row) {
		return domain.ErrFolderNameExists
	}
	row.UpdatedAt = time.Now().UTC()
	s.Folders[id] = row
	s.Writes++
	return nil
}
