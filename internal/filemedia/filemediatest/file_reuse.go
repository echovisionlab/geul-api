package filemediatest

import (
	"context"
	"slices"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

func (s *LibraryStore) FileAlreadyAttached(_ context.Context, documentID, fileID string) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	for _, r := range s.ScopedAttachments {
		if r.DocumentID == documentID && r.Attachment.FileID == fileID {
			return true, nil
		}
	}
	return false, nil
}
func (s *LibraryStore) FileUploader(_ context.Context, fileID string) (*string, error) {
	p := s.Files[fileID].Row.MemberID
	if p != nil {
		p = new(*p)
	}
	return p, s.Fail
}
func (s *LibraryStore) VerifiedFileMetadata(_ context.Context, fileID string) (domain.File, error) {
	if s.Fail != nil {
		return domain.File{}, s.Fail
	}
	row, ok := s.Files[fileID]
	if !ok || row.PendingDeletion {
		return domain.File{}, domain.ErrFileNotFound
	}
	f := deliveryFile(row)
	return domain.File{ID: f.ID, Extension: f.Extension, MimeType: f.MimeType, FileSize: f.FileSize, SHA256: slices.Clone(row.SHA256)}, nil
}
