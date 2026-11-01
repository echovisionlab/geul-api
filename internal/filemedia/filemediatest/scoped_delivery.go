package filemediatest

import (
	"context"
	"errors"
	"slices"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

type ScopedOwner struct {
	FeaturedFileID *string
	DocumentID     string
}
type ScopedAttachmentRecord struct {
	OwnerKind, OwnerID, DocumentID string
	Attachment                     domain.ScopedAttachment
}

func (s *LibraryStore) FeaturedFileID(_ context.Context, kind, id string) (*string, bool, error) {
	switch kind {
	case "page", "post":
	default:
		return nil, false, errors.New("unsupported scoped featured owner")
	}
	owner, ok := s.ScopedOwners[kind+":"+id]
	if owner.FeaturedFileID != nil {
		owner.FeaturedFileID = new(*owner.FeaturedFileID)
	}
	return owner.FeaturedFileID, ok, s.Fail
}
func (s *LibraryStore) LockScopedFiles(_ context.Context, ids []string) (map[string]domain.File, error) {
	rows := map[string]domain.File{}
	if s.Fail != nil {
		return nil, s.Fail
	}
	s.DeliveryLocks = append(s.DeliveryLocks, "scoped files")
	for id, record := range s.Files {
		if slices.Contains(ids, id) {
			row := deliveryFile(record)
			row.FolderID = nil
			row.IngestSlotID = nil
			row.IngestAttemptID = nil
			row.ClientMediaBundleID = nil
			row.CreatedAt = domain.File{}.CreatedAt
			row.UpdatedAt = domain.File{}.UpdatedAt
			rows[id] = row
		}
	}
	return rows, nil
}
func (s *LibraryStore) LockScopedAttachments(_ context.Context, kind, id, documentID string, blocks []string) (map[string]domain.ScopedAttachment, error) {
	rows := map[string]domain.ScopedAttachment{}
	if len(blocks) == 0 {
		return rows, nil
	}
	switch kind {
	case "page", "post", "program_event", "work":
	default:
		return nil, errors.New("unsupported scoped content owner")
	}
	if s.Fail != nil {
		return nil, s.Fail
	}
	s.DeliveryLocks = append(s.DeliveryLocks, "scoped attachments")
	owner, ok := s.ScopedOwners[kind+":"+id]
	if !ok || owner.DocumentID != documentID {
		return rows, nil
	}
	for _, record := range s.ScopedAttachments {
		if record.OwnerKind == kind && record.OwnerID == id && record.DocumentID == documentID && slices.Contains(blocks, record.Attachment.BlockID) {
			rows[record.Attachment.Key()] = record.Attachment
		}
	}
	return rows, nil
}
