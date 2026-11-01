package filemediatest

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

func (s *LibraryStore) LockDeliveryPrincipal(ctx context.Context, user *auth.UserInfo) (bool, error) {
	s.DeliveryLocks = append(s.DeliveryLocks, "principal")
	if s.Fail != nil {
		return false, s.Fail
	}
	return s.PrincipalFence.LockActivePrincipal(ctx, nil, user)
}
func (s *LibraryStore) LockDeliveryFiles(_ context.Context, ids []string, scope domain.DeliveryFileScope) (map[string]domain.File, error) {
	s.DeliveryLocks = append(s.DeliveryLocks, "files")
	if s.Fail != nil {
		return nil, s.Fail
	}
	if scope != domain.CatalogDelivery && scope != domain.StrongDelivery {
		return nil, fmt.Errorf("unsupported delivery File scope %d", scope)
	}
	rows := map[string]domain.File{}
	for id, record := range s.Files {
		if !slices.Contains(ids, id) {
			continue
		}
		row := deliveryFile(record)
		if scope == domain.CatalogDelivery {
			row.IngestSlotID = nil
			row.IngestAttemptID = nil
			row.ClientMediaBundleID = nil
		} else {
			row.FolderID = nil
			row.CreatedAt = time.Time{}
			row.UpdatedAt = time.Time{}
		}
		rows[id] = row
	}
	return rows, nil
}
func deliveryFile(record CatalogFileRecord) domain.File {
	r := record.Row
	row := domain.File{ID: r.ID, FileName: r.Name, FolderID: r.ParentID, UploadedByMemberID: r.MemberID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, IngestSlotID: record.IngestSlotID, IngestAttemptID: record.IngestAttemptID, ClientMediaBundleID: record.ClientMediaBundleID}
	if r.Extension != nil {
		row.Extension = *r.Extension
	}
	if r.MimeType != nil {
		row.MimeType = *r.MimeType
	}
	if r.FileSize != nil {
		row.FileSize = *r.FileSize
	}
	if r.DurationSeconds != nil {
		duration := int(*r.DurationSeconds)
		row.DurationSeconds = &duration
	}
	if record.PendingDeletion {
		at := time.Unix(1, 0).UTC()
		row.DeleteRequestedAt = &at
	}
	return row
}
func (s *LibraryStore) LockMeshDeliveryWitnesses(_ context.Context, witnesses []domain.MeshDeliveryWitness) (map[string]bool, error) {
	s.DeliveryLocks = append(s.DeliveryLocks, "mesh")
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := map[string]bool{}
	for _, w := range witnesses {
		if current, ok := s.ReadyMeshes[w.CandidateID]; ok && current == w {
			result[w.CandidateID] = true
		}
	}
	return result, nil
}
func (s *LibraryStore) LockAvatarFileBinding(_ context.Context, fileID, owner string) (bool, error) {
	s.DeliveryLocks = append(s.DeliveryLocks, "avatar")
	return s.AvatarOwners[fileID] == owner && owner != "", s.Fail
}
func (s *LibraryStore) LockUsageDeliveryFile(_ context.Context, id string) (domain.File, bool, error) {
	s.DeliveryLocks = append(s.DeliveryLocks, "files")
	if s.Fail != nil {
		return domain.File{}, false, s.Fail
	}
	record, ok := s.Files[id]
	if !ok {
		return domain.File{}, false, nil
	}
	row := deliveryFile(record)
	row.FolderID = nil
	row.UploadedByMemberID = nil
	row.CreatedAt = time.Time{}
	row.UpdatedAt = time.Time{}
	return row, true, nil
}
func (s *LibraryStore) LockDeliveryOwner(_ context.Context, kind, id string) (domain.DeliveryOwner, bool, error) {
	s.DeliveryLocks = append(s.DeliveryLocks, "owner:"+kind+"\x00"+id)
	if s.Fail != nil {
		return domain.DeliveryOwner{}, false, s.Fail
	}
	switch kind {
	case "artist", "form", "label", "page", "post", "program_event", "release", "series", "work":
	default:
		return domain.DeliveryOwner{}, false, fmt.Errorf("unsupported manage delivery usage owner %q", kind)
	}
	row, ok := s.DeliveryOwners[kind+"\x00"+id]
	if kind != "post" && kind != "program_event" {
		row.Status = ""
	}
	if kind != "post" && kind != "page" && kind != "work" && kind != "program_event" {
		row.ContentDocumentID = nil
	}
	return row, ok, nil
}
func (s *LibraryStore) LockExactDeliveryUsage(_ context.Context, w domain.DeliveryUsageWitness) (bool, error) {
	s.DeliveryLocks = append(s.DeliveryLocks, "usage:"+w.Key())
	if s.Fail != nil {
		return false, s.Fail
	}
	switch w.Kind {
	case "content_attachment", "featured_image", "artist_file", "release_file", "program_event_media", "track", "label_logo", "series_featured_image", "form_featured_image":
	default:
		return false, fmt.Errorf("unsupported manage delivery usage witness %q", w.Kind)
	}
	return s.ExactUsages[w.Key()], nil
}
