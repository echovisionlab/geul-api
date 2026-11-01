package filemediapostgres

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
)

func (s *store) FileAlreadyAttached(ctx context.Context, documentID, fileID string) (bool, error) {
	var attached bool
	err := s.tx.WithContext(ctx).Raw(`SELECT EXISTS (
 SELECT 1 FROM content_block_attachment AS cbf
 JOIN content_block AS cb ON cb.id = cbf.block_id
 WHERE cbf.selector_kind = 'active' AND cbf.file_id = ? AND cb.document_id = ?
 )`, fileID, documentID).Scan(&attached).Error
	return attached, err
}
func (s *store) FileUploader(ctx context.Context, fileID string) (*string, error) {
	var row struct {
		MemberID *string `gorm:"column:uploaded_by_member_id"`
	}
	err := s.tx.WithContext(ctx).Table("file").Select("uploaded_by_member_id").Where("id = ?", fileID).Scan(&row).Error
	return row.MemberID, err
}
func (s *store) VerifiedFileMetadata(ctx context.Context, fileID string) (domain.File, error) {
	var file model.File
	err := s.tx.WithContext(ctx).Select("id", "extension", "mime_type", "file_size", "sha256", "delete_requested_at").Where("id = ? AND delete_requested_at IS NULL", fileID).Take(&file).Error
	return domain.File(file), fileError(err)
}
