package filemediapostgres

import (
	"context"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type UploadTracks interface {
	LockExistsWithDB(context.Context, *gorm.DB, string) error
}

func (s *store) RequireUploadTrack(ctx context.Context, id string) error {
	if s.ports.UploadTracks == nil {
		return fmt.Errorf("File upload Track authority is required")
	}
	return s.ports.UploadTracks.LockExistsWithDB(ctx, s.tx, id)
}

func (s *store) InsertUploadSession(ctx context.Context, input domain.UploadSession, mode domain.UploadTargetMode) (domain.UploadSession, error) {
	row := UploadSessionRow(input)
	q := s.tx.WithContext(ctx)
	switch mode {
	case domain.UploadTargetGeneral:
		q = q.Omit("EntityID")
	case domain.UploadTargetEditorFile:
		q = q.Omit("EntityID", "EntityType", "SlotID", "ExpectedFileID")
	}
	err := q.Create(&row).Error
	return UploadSession(row), err
}

func (s *store) TrackUploadSessions(ctx context.Context, id string) ([]domain.UploadSession, error) {
	var rows []model.UploadSession
	err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("upload_type = ? AND entity_id = ?", "UPLOAD_TYPE_TRACK_AUDIO", id).Find(&rows).Error
	result := make([]domain.UploadSession, len(rows))
	for i, row := range rows {
		result[i] = UploadSession(row)
	}
	return result, err
}

func (s *store) AbortUploadSessions(ctx context.Context, ids []string, now time.Time) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	r := s.tx.WithContext(ctx).Model(&model.UploadSession{}).Where("upload_id IN ? AND status IN ?", ids, []model.UploadSessionStatus{model.UploadSessionStatusInitiated, model.UploadSessionStatusUploading, model.UploadSessionStatusFailed, model.UploadSessionStatusAborted}).Updates(structured.Fields{"status": model.UploadSessionStatusAborted, "last_activity_at": now, "updated_at": now})
	return r.RowsAffected, r.Error
}

func (s *store) HasTrackUploadSessions(ctx context.Context, id string) (bool, error) {
	var count int64
	err := s.tx.WithContext(ctx).Model(&model.UploadSession{}).Where("upload_type = ? AND entity_id = ?", "UPLOAD_TYPE_TRACK_AUDIO", id).Count(&count).Error
	return count != 0, err
}
