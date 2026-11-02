package filemediapostgres

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *store) FolderExists(ctx context.Context, id string) (bool, error) {
	var count int64
	err := s.tx.WithContext(ctx).Table("file_folder").Where("id = ?", id).Count(&count).Error
	return count != 0, err
}
func (s *store) InsertFolder(ctx context.Context, value domain.FileFolder) (domain.FileFolder, error) {
	row := fileFolderRow(value)
	err := s.tx.WithContext(ctx).Create(&row).Error
	return fileFolder(row), folderError(err)
}
func (s *store) Folder(ctx context.Context, id string) (domain.FileFolder, error) {
	return s.folder(ctx, id, false)
}
func (s *store) LockFolder(ctx context.Context, id string) (domain.FileFolder, error) {
	return s.folder(ctx, id, true)
}
func (s *store) folder(ctx context.Context, id string, lock bool) (domain.FileFolder, error) {
	var row model.FileFolder
	q := s.tx.WithContext(ctx)
	if lock {
		q = q.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	err := q.Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = storeError{err, domain.ErrFolderNotFound}
	}
	return fileFolder(row), err
}
func (s *store) LockFolderHierarchy(ctx context.Context) error {
	if s.tx.Dialector.Name() != "postgres" {
		return nil
	}
	return s.tx.WithContext(ctx).Exec("SELECT pg_advisory_xact_lock(hashtextextended('public.file_folder.hierarchy', 0))").Error
}
func (s *store) FolderMoveCreatesCycle(ctx context.Context, parentID, folderID string) (bool, error) {
	var found bool
	err := s.tx.WithContext(ctx).Raw(`
  WITH RECURSIVE ancestors AS (
   SELECT id, parent_id FROM file_folder WHERE id = ?
   UNION
   SELECT folder.id, folder.parent_id
   FROM file_folder AS folder
   JOIN ancestors ON ancestors.parent_id = folder.id
  )
  SELECT EXISTS (SELECT 1 FROM ancestors WHERE id = ?)
 `, parentID, folderID).Scan(&found).Error
	return found, err
}
func (s *store) RenameFolder(ctx context.Context, id, name string) error {
	return folderError(s.tx.WithContext(ctx).Model(&model.FileFolder{}).Where("id = ?", id).Updates(structured.Fields{"name": name, "updated_at": gorm.Expr("CURRENT_TIMESTAMP")}).Error)
}
func (s *store) MoveFolder(ctx context.Context, id string, parentID *string) error {
	return folderError(s.tx.WithContext(ctx).Model(&model.FileFolder{}).Where("id = ?", id).Updates(structured.Fields{"parent_id": parentID, "updated_at": gorm.Expr("CURRENT_TIMESTAMP")}).Error)
}
func folderError(err error) error {
	if err == nil {
		return nil
	}
	if dberrors.IsUniqueViolation(err) {
		return storeError{err, domain.ErrFolderNameExists}
	}
	if value := strings.ToLower(err.Error()); strings.Contains(value, "cycle") || strings.Contains(value, "cannot be its own parent") {
		return storeError{err, domain.ErrFolderCycle}
	}
	return err
}
