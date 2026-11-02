package filemediatest

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

func cloneUploadPointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	return new(*p)
}
func cloneUpload(s domain.UploadSession) domain.UploadSession {
	s.ClientMediaBundleID = cloneUploadPointer(s.ClientMediaBundleID)
	s.ClientMediaManifest = cloneUploadPointer(s.ClientMediaManifest)
	s.EntityType = cloneUploadPointer(s.EntityType)
	s.FileLastModified = cloneUploadPointer(s.FileLastModified)
	s.SlotID = cloneUploadPointer(s.SlotID)
	s.AttemptID = cloneUploadPointer(s.AttemptID)
	s.ExpectedFileID = cloneUploadPointer(s.ExpectedFileID)
	s.DetectedMime = cloneUploadPointer(s.DetectedMime)
	s.VerifiedAt = cloneUploadPointer(s.VerifiedAt)
	return s
}
func cloneUploads(rows map[string]domain.UploadSession) map[string]domain.UploadSession {
	result := maps.Clone(rows)
	for id, r := range result {
		result[id] = mutationtest.ClonePersisted(r)
	}
	return result
}
func cloneUploadParts(rows map[string][]domain.UploadPart) map[string][]domain.UploadPart {
	result := maps.Clone(rows)
	for id, r := range result {
		result[id] = mutationtest.ClonePersisted(r)
	}
	return result
}
func (s *LibraryStore) UploadSession(_ context.Context, upload, file string, _ bool) (domain.UploadSession, error) {
	if s.Fail != nil {
		return domain.UploadSession{}, s.Fail
	}
	r, ok := s.UploadSessions[upload]
	if !ok || r.FileID != file {
		return domain.UploadSession{}, domain.ErrUploadSessionNotFound
	}
	return cloneUpload(r), nil
}
func (s *LibraryStore) UploadParts(_ context.Context, upload string) ([]domain.UploadPart, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	rows := slices.Clone(s.UploadPartRows[upload])
	if rows == nil {
		rows = []domain.UploadPart{}
	}
	slices.SortFunc(rows, func(a, b domain.UploadPart) int { return int(a.PartNumber - b.PartNumber) })
	return rows, nil
}
func (s *LibraryStore) RenewUploadActivity(_ context.Context, upload string, now time.Time) error {
	if s.Fail != nil {
		return s.Fail
	}
	if r, ok := s.UploadSessions[upload]; ok {
		r.LastActivityAt = now
		r.UpdatedAt = now
		s.UploadSessions[upload] = r
	}
	return nil
}
func activeUploadStatus(status domain.UploadSessionStatus, partsOnly bool) bool {
	return status == domain.UploadSessionStatusInitiated || status == domain.UploadSessionStatusUploading || (!partsOnly && status == domain.UploadSessionStatusFinalizing)
}
func (s *LibraryStore) RenewActiveUploadActivity(ctx context.Context, upload, file string, partsOnly bool, now time.Time) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	r, ok := s.UploadSessions[upload]
	if !ok || r.FileID != file || !activeUploadStatus(r.Status, partsOnly) {
		return false, nil
	}
	return true, s.RenewUploadActivity(ctx, upload, now)
}
func (s *LibraryStore) NextUploadIngestSequence(_ context.Context, upload string) (int64, error) {
	if s.Fail != nil {
		return 0, s.Fail
	}
	r, ok := s.UploadSessions[upload]
	if !ok {
		return 0, domain.ErrUploadSessionNotFound
	}
	r.IngestSequence++
	s.UploadSessions[upload] = r
	return r.IngestSequence, nil
}
func (s *LibraryStore) ClaimUploadFinalizing(ctx context.Context, upload, file string, now time.Time) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	r, ok := s.UploadSessions[upload]
	if !ok || r.FileID != file || !activeUploadStatus(r.Status, false) {
		return false, nil
	}
	r.Status = domain.UploadSessionStatusFinalizing
	s.UploadSessions[upload] = r
	return true, s.RenewUploadActivity(ctx, upload, now)
}
func (s *LibraryStore) ClaimUploadAborted(ctx context.Context, upload, file string, now time.Time) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	r, ok := s.UploadSessions[upload]
	if !ok || r.FileID != file || r.Status == domain.UploadSessionStatusFinalizing {
		return false, nil
	}
	r.Status = domain.UploadSessionStatusAborted
	s.UploadSessions[upload] = r
	return true, s.RenewUploadActivity(ctx, upload, now)
}
func (s *LibraryStore) DeleteFinalizingUpload(_ context.Context, upload string) (bool, error) {
	return s.deleteUpload(upload, domain.UploadSessionStatusFinalizing)
}
func (s *LibraryStore) DeleteAbortedUpload(_ context.Context, upload string) (bool, error) {
	return s.deleteUpload(upload, domain.UploadSessionStatusAborted)
}
func (s *LibraryStore) deleteUpload(upload string, status domain.UploadSessionStatus) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	r, ok := s.UploadSessions[upload]
	if !ok || r.Status != status {
		return false, nil
	}
	delete(s.UploadSessions, upload)
	delete(s.UploadPartRows, upload)
	return true, nil
}
