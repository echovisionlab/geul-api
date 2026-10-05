package public

import (
	"context"
	"strings"

	"gorm.io/gorm"
)

func loadTrackOriginalFiles(ctx context.Context, db *gorm.DB, fileIDs []string) (map[string]scopedMediaFile, error) {
	result := make(map[string]scopedMediaFile)
	ids := uniqueNonEmptyIDs(fileIDs)
	if len(ids) == 0 {
		return result, nil
	}
	var rows []scopedMediaFile
	if err := db.WithContext(ctx).
		Table("file").
		Select("id", "extension", "mime_type", "file_size", "file_name").
		Where("id IN ? AND delete_requested_at IS NULL", ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		if extension := strings.ToLower(strings.TrimSpace(row.Extension)); extension != "" {
			row.Extension = extension
			result[row.ID] = row
		}
	}
	return result, nil
}
