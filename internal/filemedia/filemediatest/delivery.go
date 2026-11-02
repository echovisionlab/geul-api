package filemediatest

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

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
		if record.DeleteRequestedAt != nil {
			row.DeleteRequestedAt = record.DeleteRequestedAt
		}
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

func (s *LibraryStore) DeliveryFileOwnership(_ context.Context, ids []string) ([]domain.File, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := make([]domain.File, 0, len(ids))
	for _, id := range ids {
		if r, ok := s.Files[id]; ok {
			f := deliveryFile(r)
			result = append(result, domain.File{ID: id, UploadedByMemberID: f.UploadedByMemberID, DeleteRequestedAt: f.DeleteRequestedAt})
		}
	}
	return result, nil
}
func (s *LibraryStore) DeliveryIngestBindings(_ context.Context, ids []string) ([]domain.IngestBinding, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := make([]domain.IngestBinding, 0, len(ids))
	for _, id := range ids {
		if b, ok := s.IngestBindings[id]; ok {
			result = append(result, b)
		}
	}
	return result, nil
}
func (s *LibraryStore) CurrentDeliveryUsages(_ context.Context, ids []string) ([]domain.CurrentDeliveryUsage, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := []domain.CurrentDeliveryUsage{}
	for _, u := range s.DeliveryUsages {
		if slices.Contains(ids, u.FileID) {
			result = append(result, u)
		}
	}
	return result, nil
}

func (s *LibraryStore) StoredFileDeliveries(_ context.Context, ids []string, withIngest bool) ([]domain.StoredFileDelivery, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := []domain.StoredFileDelivery{}
	for _, id := range ids {
		r, ok := s.Files[id]
		if !ok || r.PendingDeletion {
			continue
		}
		f := deliveryFile(r)
		row := domain.StoredFileDelivery{ID: id, FileName: &f.FileName, Extension: f.Extension, MimeType: f.MimeType, FileSize: f.FileSize, DurationSeconds: f.DurationSeconds}
		if withIngest {
			row.IngestSlotID = f.IngestSlotID
			row.IngestAttemptID = f.IngestAttemptID
			row.ClientMediaBundleID = f.ClientMediaBundleID
		}
		result = append(result, row)
	}
	return result, nil
}
func (s *LibraryStore) StoredDerivativeDeliveries(ctx context.Context, ids []string) ([]domain.StoredDerivativeDelivery, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := []domain.StoredDerivativeDelivery{}
	for _, id := range ids {
		for _, row := range s.Derivatives[id] {
			if row.AssetID != nil {
				if asset, err := s.MediaAssets.Asset(ctx, *row.AssetID); err == nil {
					row.AssetKind, row.AssetExtension, row.AssetMimeType = new(asset.Kind), new(asset.Extension), new(asset.MimeType)
					row.AssetFileSize, row.AssetSHA256 = cloneUploadPointer(asset.FileSize), slices.Clone(asset.SHA256)
					row.AssetDisposition, row.AssetDownloadFilename = new(asset.Disposition), cloneUploadPointer(asset.DownloadFilename)
					row.AssetStatus = new(string(asset.Status))
				}
			}
			if row.MediaGenerationID != nil {
				if generation, err := s.MediaAssets.Generation(ctx, *row.MediaGenerationID); err == nil {
					row.MediaGenerationFileID, row.MediaGenerationManifest = new(generation.FileID), new(generation.ManifestName)
					row.MediaGenerationStatus = new(string(generation.Status))
				}
			}
			result = append(result, row)
		}
	}
	return result, nil
}
func (s *LibraryStore) MeshDeliveryOutputs(_ context.Context, id string) ([]domain.MeshDeliveryOutput, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := []domain.MeshDeliveryOutput{}
	for _, r := range s.MeshRecords {
		w := r.Witness
		if w.SourceFileID == id && w.OutputFileID != "" {
			out := w.OutputFileID
			result = append(result, domain.MeshDeliveryOutput{ID: w.CandidateID, Status: r.Status, OutputFileID: &out})
		}
	}
	slices.SortFunc(result, func(a, b domain.MeshDeliveryOutput) int { return cmp.Compare(b.ID, a.ID) })
	return result, nil
}
func (s *LibraryStore) SourceAssetReferences(ctx context.Context, cdn, kind string, ids []string) (map[string]*mediaasset.Reference, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	return mediaasset.LoadReadyPublicAssetRefsForSourceFiles(ctx, mutationtest.New[mediaasset.Store](s.MediaAssets), cdn, kind, ids)
}
func (s *LibraryStore) DeliveryProcessingFailures(_ context.Context, audio, video []string) (domain.ProcessingFailureCounts, error) {
	filter := func(counts map[string]int64, ids []string) map[string]int64 {
		m := map[string]int64{}
		for _, id := range ids {
			if n := counts[id]; n > 0 {
				m[id] = n
			}
		}
		return m
	}
	return domain.ProcessingFailureCounts{Audio: filter(s.Failures.Audio, audio), Video: filter(s.Failures.Video, video), Waveform: filter(s.Failures.Waveform, audio)}, s.Fail
}
