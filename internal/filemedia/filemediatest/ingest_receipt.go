package filemediatest

import (
	"context"
	"slices"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

func (s *LibraryStore) HasFileUploadSessions(_ context.Context, id string) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	for _, row := range s.UploadSessions {
		if row.FileID == id {
			return true, nil
		}
	}
	return false, nil
}

func (s *LibraryStore) IngestFile(_ context.Context, id string) (domain.File, error) {
	if s.Fail != nil {
		return domain.File{}, s.Fail
	}
	r, ok := s.Files[id]
	if !ok {
		return domain.File{}, domain.ErrFileNotFound
	}
	row := deliveryFile(r)
	row.SHA256 = slices.Clone(r.SHA256)
	row.FolderID = cloneUploadPointer(row.FolderID)
	row.UploadedByMemberID = cloneUploadPointer(row.UploadedByMemberID)
	row.IngestSlotID = cloneUploadPointer(row.IngestSlotID)
	row.IngestAttemptID = cloneUploadPointer(row.IngestAttemptID)
	row.ClientMediaBundleID = cloneUploadPointer(row.ClientMediaBundleID)
	row.DeleteRequestedAt = cloneUploadPointer(row.DeleteRequestedAt)
	return row, nil
}

func (s *LibraryStore) IngestBinding(_ context.Context, id string) (domain.IngestBinding, error) {
	if s.Fail != nil {
		return domain.IngestBinding{}, s.Fail
	}
	row, ok := s.IngestBindings[id]
	if !ok {
		return domain.IngestBinding{}, domain.ErrIngestBindingNotFound
	}
	row.EntityType = cloneUploadPointer(row.EntityType)
	return row, nil
}

func (s *LibraryStore) FailFinalizingUpload(_ context.Context, id string, now time.Time) error {
	if s.Fail != nil {
		return s.Fail
	}
	row, ok := s.UploadSessions[id]
	if ok && row.Status == domain.UploadSessionStatusFinalizing {
		row.Status = domain.UploadSessionStatusFailed
		row.LastActivityAt, row.UpdatedAt = now, now
		s.UploadSessions[id] = row
	}
	return nil
}
