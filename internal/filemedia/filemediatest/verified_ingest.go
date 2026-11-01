package filemediatest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/google/uuid"
)

func (s *LibraryStore) EnsureIngestDateFolder(_ context.Context) (*string, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var parent *string
	now := time.Now().UTC()
	for _, name := range []string{now.Format("2006"), now.Format("01"), now.Format("02")} {
		found := ""
		for id, row := range s.Folders {
			if row.Name == name && sameParent(row.ParentID, parent) {
				found = id
				break
			}
		}
		if found == "" {
			found = uuid.NewString()
			s.Folders[found] = domain.FileFolder{ID: found, Name: name, ParentID: cloneUploadPointer(parent), CreatedAt: now, UpdatedAt: now}
		}
		parent = &found
	}
	return parent, nil
}

func (s *LibraryStore) InsertIngestFile(_ context.Context, file domain.File) error {
	if s.Fail != nil {
		return s.Fail
	}
	if _, exists := s.Files[file.ID]; exists {
		return errors.New("File already exists")
	}
	if file.SHA256 != nil && len(file.SHA256) != 32 {
		return errors.New("File SHA256 length is invalid")
	}
	if file.FolderID != nil {
		if _, ok := s.Folders[*file.FolderID]; !ok {
			return domain.ErrFolderNotFound
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	s.Files[file.ID] = CatalogFileRecord{Row: domain.CatalogRow{ItemType: "file", ID: file.ID, Name: file.FileName, Extension: new(file.Extension), MimeType: new(file.MimeType), FileSize: new(file.FileSize), ParentID: cloneUploadPointer(file.FolderID), MemberID: cloneUploadPointer(file.UploadedByMemberID), CreatedAt: now, UpdatedAt: now}, SHA256: slices.Clone(file.SHA256), IngestSlotID: cloneUploadPointer(file.IngestSlotID), IngestAttemptID: cloneUploadPointer(file.IngestAttemptID)}
	return nil
}

func (s *LibraryStore) InsertIngestBinding(_ context.Context, input domain.IngestBinding, independent bool) error {
	if s.Fail != nil {
		return s.Fail
	}
	if _, ok := s.Files[input.FileID]; !ok {
		return domain.ErrFileNotFound
	}
	if _, ok := s.IngestBindings[input.FileID]; ok {
		return errors.New("File ingest binding already exists")
	}
	if independent {
		input.EntityID = ""
	}
	input.EntityType = cloneUploadPointer(input.EntityType)
	if input.CreatedAt.IsZero() {
		input.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	}
	s.IngestBindings[input.FileID] = input
	return nil
}

func (s *LibraryStore) LockIngestOwner(_ context.Context, kind, id string) error {
	if s.Fail != nil {
		return s.Fail
	}
	switch kind {
	case "artist", "form", "label", "page", "post", "program_event", "release", "series", "work":
	default:
		return nil
	}
	if strings.TrimSpace(id) == "" {
		return nil
	}
	if _, ok := s.ScopedOwners[kind+":"+id]; ok {
		return nil
	}
	if _, ok := s.DeliveryOwners[kind+"\x00"+id]; ok {
		return nil
	}
	return domainerr.ResourceNotFound(kind, id)
}
