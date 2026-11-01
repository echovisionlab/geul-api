package filemediapostgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *store) FeaturedFileID(ctx context.Context, owner, id string) (*string, bool, error) {
	switch owner {
	case "page", "post":
	default:
		return nil, false, errors.New("unsupported scoped featured owner")
	}
	var slot struct {
		FileID *string `gorm:"column:featured_image_file_id"`
	}
	err := s.tx.WithContext(ctx).Table(owner).Select("featured_image_file_id").Where("id = ?", id).Take(&slot).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	return slot.FileID, err == nil, err
}
func (s *store) LockScopedFiles(
	ctx context.Context,
	fileIDs []string,
) (map[string]domain.File, error) {
	tx := s.tx
	result := make(map[string]domain.File, len(fileIDs))
	if len(fileIDs) == 0 {
		return result, nil
	}
	var files []model.File
	query := tx.WithContext(ctx).
		Table("file").
		Select("id", "file_name", "extension", "mime_type", "file_size", "duration_seconds", "uploaded_by_member_id", "delete_requested_at").
		Where("id IN ?", fileIDs).
		Order("id ASC")
	if tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
	}
	if err := query.Find(&files).Error; err != nil {
		return nil, fmt.Errorf("lock scoped delivery Files: %w", err)
	}
	for _, file := range files {
		result[file.ID] = domain.File(file)
	}
	return result, nil
}

func (s *store) LockScopedAttachments(
	ctx context.Context,
	owner string,
	ownerID string,
	documentID string,
	blockIDs []string,
) (map[string]domain.ScopedAttachment, error) {
	tx := s.tx
	result := make(map[string]domain.ScopedAttachment)
	if len(blockIDs) == 0 {
		return result, nil
	}
	ownerTable := string(owner)
	switch owner {
	case "page", "post", "program_event", "work":
	default:
		return nil, errors.New("unsupported scoped content owner")
	}
	var rows []domain.ScopedAttachment
	query := tx.WithContext(ctx).
		Table("content_block_attachment AS attachment").
		Select("attachment.block_id::text AS block_id", "attachment.reference_path", "attachment.file_id::text AS file_id").
		Joins("JOIN content_block AS block ON block.id = attachment.block_id").
		Joins("JOIN "+ownerTable+" AS owner ON owner.content_document_id = block.document_id").
		Where("owner.id = ? AND owner.content_document_id = ? AND attachment.selector_kind = 'active' AND attachment.block_id IN ?", ownerID, documentID, blockIDs).
		Order("attachment.block_id ASC, attachment.reference_path ASC")
	if tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthShare, Table: clause.Table{Name: "attachment"}})
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("lock exact scoped content attachments: %w", err)
	}
	for _, row := range rows {
		result[row.Key()] = row
	}
	return result, nil
}
