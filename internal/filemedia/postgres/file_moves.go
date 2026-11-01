package filemediapostgres

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *store) LockFileForRename(ctx context.Context, id string) (domain.File, error) {
	var row model.File
	err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id", "file_name", "extension", "delete_requested_at").Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = storeError{err, domain.ErrFileNotFound}
	}
	return domain.File(row), err
}
func (s *store) LockFilesForMove(ctx context.Context, ids []string) ([]domain.File, error) {
	var rows []model.File
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id", "folder_id", "delete_requested_at").Where("id IN ?", ids).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]domain.File, len(rows))
	for i, row := range rows {
		result[i] = domain.File(row)
	}
	return result, nil
}
func (s *store) RenameFile(ctx context.Context, id, name string) error {
	return s.tx.WithContext(ctx).Table("file").Where("id = ?", id).Updates(structured.Fields{"file_name": name, "updated_at": gorm.Expr("CURRENT_TIMESTAMP")}).Error
}
func (s *store) MoveFile(ctx context.Context, id string, parent *string) error {
	return s.tx.WithContext(ctx).Table("file").Where("id = ?", id).Updates(structured.Fields{"folder_id": parent, "updated_at": gorm.Expr("CURRENT_TIMESTAMP")}).Error
}
func (s *store) FileCount(ctx context.Context, ids []string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).Table("file").Where("id IN ?", ids).Count(&count).Error
	return count, err
}
