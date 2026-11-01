package filemediatest

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

func (s *LibraryStore) LockFileForRename(_ context.Context, id string) (domain.File, error) {
	if s.Fail != nil {
		return domain.File{}, s.Fail
	}
	record, ok := s.Files[id]
	if !ok {
		return domain.File{}, domain.ErrFileNotFound
	}
	row := domain.File{ID: id, FileName: record.Row.Name}
	if record.Row.Extension != nil {
		row.Extension = *record.Row.Extension
	}
	if record.PendingDeletion {
		at := time.Now().UTC()
		row.DeleteRequestedAt = &at
	}
	return row, nil
}
func (s *LibraryStore) LockFilesForMove(_ context.Context, ids []string) ([]domain.File, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []domain.File
	for id, record := range s.Files {
		if !slices.Contains(ids, id) {
			continue
		}
		row := domain.File{ID: id, FolderID: record.Row.ParentID}
		if record.PendingDeletion {
			at := time.Now().UTC()
			row.DeleteRequestedAt = &at
		}
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(a, b domain.File) int { return strings.Compare(a.ID, b.ID) })
	return rows, nil
}
func (s *LibraryStore) RenameFile(_ context.Context, id, name string) error {
	if s.Fail != nil {
		return s.Fail
	}
	record, ok := s.Files[id]
	if !ok {
		return nil
	}
	record.Row.Name = name
	record.Row.UpdatedAt = time.Now().UTC()
	s.Files[id] = record
	s.Writes++
	return nil
}
func (s *LibraryStore) MoveFile(_ context.Context, id string, parent *string) error {
	if s.Fail != nil {
		return s.Fail
	}
	record, ok := s.Files[id]
	if !ok {
		return nil
	}
	record.Row.ParentID = parent
	record.Row.UpdatedAt = time.Now().UTC()
	s.Files[id] = record
	s.Writes++
	return nil
}
func (s *LibraryStore) FileCount(_ context.Context, ids []string) (int64, error) {
	if s.Fail != nil {
		return 0, s.Fail
	}
	var count int64
	for id := range s.Files {
		if slices.Contains(ids, id) {
			count++
		}
	}
	return count, nil
}
