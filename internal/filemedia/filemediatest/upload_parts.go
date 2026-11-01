package filemediatest

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

func (s *LibraryStore) WritableUploadSession(ctx context.Context, upload, file string) (domain.UploadSession, error) {
	row, err := s.UploadSession(ctx, upload, file, true)
	if err != nil {
		return row, err
	}
	if !activeUploadStatus(row.Status, true) {
		return domain.UploadSession{}, domain.ErrUploadSessionNotFound
	}
	return row, nil
}
func (s *LibraryStore) MarkUploadPartUploading(ctx context.Context, upload, file string, now time.Time) (bool, error) {
	changed, err := s.RenewActiveUploadActivity(ctx, upload, file, true, now)
	if err != nil || !changed {
		return changed, err
	}
	row := s.UploadSessions[upload]
	row.Status = domain.UploadSessionStatusUploading
	s.UploadSessions[upload] = row
	return true, nil
}
func (s *LibraryStore) UpsertUploadPart(_ context.Context, row domain.UploadPart) error {
	if s.Fail != nil {
		return s.Fail
	}
	if _, ok := s.UploadSessions[row.UploadID]; !ok {
		return errors.New("upload part session is missing")
	}
	if row.PartNumber < 1 || row.Size < 0 {
		return errors.New("upload part shape is invalid")
	}
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	}
	rows := s.UploadPartRows[row.UploadID]
	for i, old := range rows {
		if old.PartNumber == row.PartNumber {
			row.CreatedAt = old.CreatedAt
			rows[i] = row
			s.UploadPartRows[row.UploadID] = rows
			return nil
		}
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	}
	s.UploadPartRows[row.UploadID] = append(rows, row)
	return nil
}
func (s *LibraryStore) DeleteUploadPart(_ context.Context, upload string, part int32) error {
	if s.Fail != nil {
		return s.Fail
	}
	rows := s.UploadPartRows[upload]
	s.UploadPartRows[upload] = slices.DeleteFunc(rows, func(r domain.UploadPart) bool { return r.PartNumber == part })
	return nil
}
func (s *LibraryStore) ReplaceUploadParts(ctx context.Context, upload string, parts []domain.UploadPart) error {
	if s.Fail != nil {
		return s.Fail
	}
	delete(s.UploadPartRows, upload)
	seen := map[string]map[int32]bool{}
	for _, p := range parts {
		if seen[p.UploadID] == nil {
			seen[p.UploadID] = map[int32]bool{}
		}
		if seen[p.UploadID][p.PartNumber] {
			return errors.New("duplicate upload part")
		}
		for _, existing := range s.UploadPartRows[p.UploadID] {
			if existing.PartNumber == p.PartNumber {
				return errors.New("duplicate upload part")
			}
		}
		seen[p.UploadID][p.PartNumber] = true
		if err := s.UpsertUploadPart(ctx, p); err != nil {
			return err
		}
	}
	return nil
}
func (s *LibraryStore) DetectUploadMIME(_ context.Context, upload, file, mime string, now time.Time) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	r, ok := s.UploadSessions[upload]
	if !ok || r.FileID != file || !activeUploadStatus(r.Status, true) || (r.DetectedMime != nil && *r.DetectedMime != "") {
		return false, nil
	}
	r.DetectedMime = &mime
	r.VerifiedAt = &now
	r.UpdatedAt = now
	s.UploadSessions[upload] = r
	return true, nil
}
func (s *LibraryStore) UploadMIMEState(ctx context.Context, upload, file string) (domain.UploadMIMEState, error) {
	r, err := s.UploadSession(ctx, upload, file, false)
	return domain.UploadMIMEState{Status: r.Status, DetectedMime: r.DetectedMime}, err
}
