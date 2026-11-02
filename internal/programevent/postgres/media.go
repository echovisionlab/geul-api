package programeventpostgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func mediaFromRow(row model.ProgramEventMedia) programevent.Media {
	return programevent.Media{
		ID: row.ID, EventID: row.EventID, FileID: row.FileID, Role: row.Role, SortOrder: row.SortOrder,
		IsPrimary: row.IsPrimary, Alt: row.Alt, Caption: row.Caption, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func mediaRows(rows []model.ProgramEventMedia) []programevent.Media {
	media := make([]programevent.Media, 0, len(rows))
	for _, row := range rows {
		media = append(media, mediaFromRow(row))
	}
	return media
}

func (s *store) MediaOfFile(ctx context.Context, eventID, role, fileID string) (programevent.Media, bool, error) {
	var row model.ProgramEventMedia
	result := s.tx.WithContext(ctx).
		Where("event_id = ? AND role = ? AND file_id = ?", eventID, role, fileID).
		First(&row)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return programevent.Media{}, false, nil
		}
		return programevent.Media{}, false, result.Error
	}
	return mediaFromRow(row), true, nil
}

func (s *store) MediaByID(ctx context.Context, eventID, mediaID string) (programevent.Media, error) {
	var row model.ProgramEventMedia
	if err := s.tx.WithContext(ctx).First(&row, "id = ? AND event_id = ?", mediaID, eventID).Error; err != nil {
		return programevent.Media{}, notFound(err, programevent.ErrMediaNotFound)
	}
	return mediaFromRow(row), nil
}

func (s *store) PrimaryMediaFileID(ctx context.Context, eventID, role string) (string, error) {
	var row struct {
		FileID string `gorm:"column:file_id"`
	}
	if err := s.tx.WithContext(ctx).Raw(`SELECT file_id::text FROM program_event_media WHERE event_id = ? AND role = ? ORDER BY is_primary DESC, sort_order ASC, created_at ASC LIMIT 1`, eventID, role).Scan(&row).Error; err != nil {
		return "", err
	}
	return row.FileID, nil
}

func (s *store) PosterFileIDs(ctx context.Context, eventIDs []string) (map[string]string, error) {
	result := make(map[string]string, len(eventIDs))
	if len(eventIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		EventID string `gorm:"column:event_id"`
		FileID  string `gorm:"column:file_id"`
	}
	if err := s.tx.WithContext(ctx).Raw(
		`SELECT DISTINCT ON (event_id)
		     event_id::text,
		     file_id::text
		 FROM program_event_media
		 WHERE event_id::text IN ? AND role = 'poster'
		 ORDER BY event_id, is_primary DESC, sort_order ASC, created_at ASC`,
		eventIDs,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		result[rows[i].EventID] = rows[i].FileID
	}
	return result, nil
}

func (s *store) HasPrimaryMedia(ctx context.Context, eventID, role string) (bool, error) {
	var count int64
	if err := s.tx.WithContext(ctx).Model(&model.ProgramEventMedia{}).
		Where("event_id = ? AND role = ? AND is_primary = TRUE", eventID, role).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *store) ClearPrimaryMedia(ctx context.Context, eventID, role string, updatedAt time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.ProgramEventMedia{}).
		Where("event_id = ? AND role = ?", eventID, role).
		Updates(structured.Fields{
			"is_primary": false,
			"updated_at": updatedAt,
		}).Error
}

func (s *store) UpdateMedia(ctx context.Context, mediaID string, alt, caption *string, makePrimary bool, updatedAt time.Time) error {
	updates := structured.Fields{
		"alt":        alt,
		"caption":    caption,
		"updated_at": updatedAt,
	}
	if makePrimary {
		updates["is_primary"] = true
	}
	return s.tx.WithContext(ctx).Model(&model.ProgramEventMedia{ID: mediaID}).Updates(updates).Error
}

func (s *store) NextMediaSortOrder(ctx context.Context, eventID, role string) (int32, error) {
	var next struct {
		SortOrder int32 `gorm:"column:sort_order"`
	}
	if err := s.tx.WithContext(ctx).
		Table("program_event_media").
		Select("COALESCE(MAX(sort_order), -1) + 1 AS sort_order").
		Where("event_id = ? AND role = ?", eventID, role).
		Scan(&next).Error; err != nil {
		return 0, err
	}
	return next.SortOrder, nil
}

func (s *store) InsertMedia(ctx context.Context, media *programevent.Media) error {
	row := &model.ProgramEventMedia{
		EventID: media.EventID, FileID: media.FileID, Role: media.Role, SortOrder: media.SortOrder,
		IsPrimary: media.IsPrimary, Alt: media.Alt, Caption: media.Caption,
		CreatedAt: media.CreatedAt, UpdatedAt: media.UpdatedAt,
	}
	if err := s.tx.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(row).Error; err != nil {
		return err
	}
	*media = mediaFromRow(*row)
	return nil
}

func (s *store) DeleteMedia(ctx context.Context, eventID, mediaID string) (bool, error) {
	result := s.tx.WithContext(ctx).Delete(&model.ProgramEventMedia{}, "id = ? AND event_id = ?", mediaID, eventID)
	return result.RowsAffected > 0, result.Error
}

func (s *store) FirstMedia(ctx context.Context, eventID, role string) (programevent.Media, bool, error) {
	var replacement model.ProgramEventMedia
	if err := s.tx.WithContext(ctx).Where("event_id = ? AND role = ?", eventID, role).
		Order("sort_order ASC, created_at ASC").First(&replacement).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return programevent.Media{}, false, nil
		}
		return programevent.Media{}, false, err
	}
	return mediaFromRow(replacement), true, nil
}

func (s *store) MarkMediaPrimary(ctx context.Context, mediaID string, updatedAt time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.ProgramEventMedia{ID: mediaID}).Updates(structured.Fields{
		"is_primary": true,
		"updated_at": updatedAt,
	}).Error
}

func (s *store) RoleMedia(ctx context.Context, eventID, role string) ([]programevent.Media, error) {
	var rows []model.ProgramEventMedia
	if err := s.tx.WithContext(ctx).
		Where("event_id = ? AND role = ?", eventID, role).
		Order("sort_order ASC, created_at ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return mediaRows(rows), nil
}

func (s *store) PlaceMedia(ctx context.Context, eventID, role, mediaID string, position int32, updatedAt time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Model(&model.ProgramEventMedia{}).
		Where("id = ? AND event_id = ? AND role = ?", mediaID, eventID, role).
		Updates(structured.Fields{
			"sort_order": position,
			"is_primary": position == 0,
			"updated_at": updatedAt,
		})
	return result.RowsAffected > 0, result.Error
}

func (s *store) DeleteRoleMedia(ctx context.Context, eventID, role string) ([]programevent.Media, error) {
	var rows []model.ProgramEventMedia
	if err := s.tx.WithContext(ctx).
		Where("event_id = ? AND role = ?", eventID, role).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	if err := s.tx.WithContext(ctx).
		Where("event_id = ? AND role = ?", eventID, role).
		Delete(&model.ProgramEventMedia{}).Error; err != nil {
		return nil, err
	}
	return mediaRows(rows), nil
}

func (s *store) EventMedia(ctx context.Context, eventID string) ([]programevent.Media, error) {
	var rows []model.ProgramEventMedia
	if err := s.tx.WithContext(ctx).
		Where("event_id = ?", eventID).
		Order("role ASC, is_primary DESC, sort_order ASC, created_at ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return mediaRows(rows), nil
}
