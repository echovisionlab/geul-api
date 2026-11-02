package mediaassetpostgres

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func NewAttachmentStore(tx *gorm.DB) mediaasset.AttachmentStore {
	if tx == nil {
		panic("version attachment transaction is required")
	}
	return &store{tx: tx}
}
func (s *store) LockVersionFiles(ctx context.Context, ids []string) ([]mediaasset.VersionFile, error) {
	var rows []struct {
		ID                string     `gorm:"column:id"`
		MIMEType          string     `gorm:"column:mime_type"`
		DeleteRequestedAt *time.Time `gorm:"column:delete_requested_at"`
	}
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).Table("file").Select("id, mime_type, delete_requested_at").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]mediaasset.VersionFile, len(rows))
	for i, row := range rows {
		result[i] = mediaasset.VersionFile{ID: row.ID, MIMEType: row.MIMEType, DeleteRequestedAt: row.DeleteRequestedAt}
	}
	return result, nil
}
