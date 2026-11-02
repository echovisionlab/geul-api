package filemediatest

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

func (s *LibraryStore) RequireUploadTrack(_ context.Context, id string) error {
	if s.Fail != nil {
		return s.Fail
	}
	if !s.UploadTracks[id] {
		return domainerr.ResourceNotFound("track", id)
	}
	return nil
}

func (s *LibraryStore) InsertUploadSession(_ context.Context, input domain.UploadSession, mode domain.UploadTargetMode) (domain.UploadSession, error) {
	if s.Fail != nil {
		return domain.UploadSession{}, s.Fail
	}
	if _, exists := s.UploadSessions[input.UploadID]; exists {
		return domain.UploadSession{}, errors.New("upload session already exists")
	}
	if input.CreatedAt.IsZero() {
		input.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	}
	if input.UpdatedAt.IsZero() {
		input.UpdatedAt = input.CreatedAt
	}
	if input.LastActivityAt.IsZero() {
		input.LastActivityAt = input.CreatedAt
	}
	stored := cloneUpload(input)
	switch mode {
	case domain.UploadTargetGeneral:
		stored.EntityID = ""
	case domain.UploadTargetEditorFile:
		stored.EntityID = ""
		stored.EntityType, stored.SlotID, stored.ExpectedFileID = nil, nil, nil
	}
	s.UploadSessions[input.UploadID] = stored
	return cloneUpload(input), nil
}

func (s *LibraryStore) TrackUploadSessions(_ context.Context, id string) ([]domain.UploadSession, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := []domain.UploadSession{}
	for _, row := range s.UploadSessions {
		if row.UploadType == "UPLOAD_TYPE_TRACK_AUDIO" && row.EntityID == id {
			result = append(result, cloneUpload(row))
		}
	}
	return result, nil
}

func (s *LibraryStore) AbortUploadSessions(_ context.Context, ids []string, now time.Time) (int64, error) {
	if s.Fail != nil {
		return 0, s.Fail
	}
	var count int64
	for _, id := range ids {
		row, ok := s.UploadSessions[id]
		if !ok || (row.Status != domain.UploadSessionStatusInitiated && row.Status != domain.UploadSessionStatusUploading && row.Status != domain.UploadSessionStatusFailed && row.Status != domain.UploadSessionStatusAborted) {
			continue
		}
		row.Status = domain.UploadSessionStatusAborted
		row.LastActivityAt, row.UpdatedAt = now, now
		s.UploadSessions[id] = row
		count++
	}
	return count, nil
}

func (s *LibraryStore) HasTrackUploadSessions(ctx context.Context, id string) (bool, error) {
	rows, err := s.TrackUploadSessions(ctx, id)
	return len(rows) != 0, err
}
