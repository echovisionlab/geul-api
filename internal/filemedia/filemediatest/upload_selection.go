package filemediatest

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

func independentUpload(kind string) bool {
	switch kind {
	case "UPLOAD_TYPE_GENERAL_FILE", "UPLOAD_TYPE_EDITOR_IMAGE", "UPLOAD_TYPE_EDITOR_AUDIO", "UPLOAD_TYPE_EDITOR_VIDEO", "UPLOAD_TYPE_EDITOR_ATTACHMENT", "UPLOAD_TYPE_EDITOR_MESH":
		return true
	}
	return false
}
func sameUploadPointer(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func (s *LibraryStore) uploadSelections(f domain.UploadSearch, candidate bool) []domain.UploadSession {
	rows := []domain.UploadSession{}
	for _, r := range s.UploadSessions {
		if r.UploadType != f.UploadType {
			continue
		}
		eligible := r.Status == domain.UploadSessionStatusFinalizing || ((r.Status == domain.UploadSessionStatusInitiated || r.Status == domain.UploadSessionStatusUploading) && !r.LastActivityAt.Before(f.ActiveAfter) && r.ChunkSize == f.ChunkSize)
		if !eligible {
			continue
		}
		if independentUpload(f.UploadType) {
			if r.EntityID != "" {
				continue
			}
		} else if r.EntityID != f.EntityID {
			continue
		}
		if f.EntityType != nil && (r.EntityType == nil || *r.EntityType != *f.EntityType) {
			continue
		}
		if !sameUploadPointer(r.ExpectedFileID, f.ExpectedCurrentFileID) {
			continue
		}
		if f.Mode == domain.UploadTargetEditorFile || f.Mode == domain.UploadTargetTrackProjection || (f.Mode == domain.UploadTargetGeneral && f.UploadType == "UPLOAD_TYPE_GENERAL_FILE") {
			if r.SlotID != nil {
				continue
			}
		} else if f.SlotID != "" && (r.SlotID == nil || *r.SlotID != f.SlotID) {
			continue
		}
		if candidate {
			if f.FileID != "" && r.FileID != f.FileID || f.UploadID != "" && r.UploadID != f.UploadID || f.FileName != "" && r.FileName != f.FileName || f.MimeType != "" && r.RequestedMime != f.MimeType {
				continue
			}
			if f.FileSize != nil && *f.FileSize > 0 && r.FileSize != *f.FileSize {
				continue
			}
			if f.FileLastModified != nil && (r.FileLastModified == nil || *r.FileLastModified != *f.FileLastModified) {
				continue
			}
		}
		rows = append(rows, cloneUpload(r))
	}
	slices.SortFunc(rows, func(a, b domain.UploadSession) int {
		if (a.Status == domain.UploadSessionStatusFinalizing) != (b.Status == domain.UploadSessionStatusFinalizing) {
			if a.Status == domain.UploadSessionStatusFinalizing {
				return -1
			}
			return 1
		}
		if n := b.LastActivityAt.Compare(a.LastActivityAt); n != 0 {
			return n
		}
		return cmp.Compare(a.UploadID, b.UploadID)
	})
	return rows
}
func (s *LibraryStore) ActiveSurfaceUploads(_ context.Context, f domain.UploadSearch) ([]domain.UploadSession, error) {
	if independentUpload(f.UploadType) || f.Mode == domain.UploadTargetUnknown || (f.Mode == domain.UploadTargetGeneral && f.SlotID == "") {
		return nil, nil
	}
	if s.Fail != nil {
		return nil, s.Fail
	}
	return s.uploadSelections(f, false), nil
}
func (s *LibraryStore) FindUploadCandidate(_ context.Context, f domain.UploadSearch) (domain.UploadSession, bool, error) {
	if f.Mode == domain.UploadTargetUnknown {
		return domain.UploadSession{}, false, fmt.Errorf("upload target classification is missing")
	}
	if s.Fail != nil {
		return domain.UploadSession{}, false, s.Fail
	}
	rows := s.uploadSelections(f, true)
	if len(rows) == 0 {
		return domain.UploadSession{}, false, nil
	}
	return rows[0], true, nil
}
