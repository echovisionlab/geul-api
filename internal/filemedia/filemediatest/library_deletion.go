package filemediatest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	telemetry "github.com/echovisionlab/geul-telemetry"
)

func (s *LibraryStore) PutMediaAsset(asset mediaasset.Asset) {
	s.MediaAssets.PutAsset(asset)
	s.AssetIDs[asset.ID] = true
}
func (s *LibraryStore) PutMediaGeneration(g mediaasset.Generation) {
	s.MediaAssets.PutGeneration(g)
	s.Generations[g.ID] = g
}
func (s *LibraryStore) LockDeletionCandidate(_ context.Context, id string) (domain.File, error) {
	if s.Fail != nil {
		return domain.File{}, s.Fail
	}
	r, ok := s.Files[id]
	if !ok {
		return domain.File{}, domain.ErrFileNotFound
	}
	f := deliveryFile(r)
	return domain.File{ID: id, DeleteRequestedAt: f.DeleteRequestedAt}, nil
}
func (s *LibraryStore) LockDeletionCandidates(ctx context.Context, ids []string) ([]domain.File, error) {
	result := []domain.File{}
	for _, id := range slices.Compact(slices.Sorted(slices.Values(ids))) {
		f, err := s.LockDeletionCandidate(ctx, id)
		if errors.Is(err, domain.ErrFileNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, f)
	}
	return result, nil
}
func (s *LibraryStore) ActiveFileReferenceNames(_ context.Context, id string) ([]string, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	names := slices.Clone(s.References[id])
	for _, m := range s.MeshRecords {
		if m.Witness.OutputFileID == id && slices.Contains(domain.MeshOutputProtectionStatuses(), m.Status) {
			name := "mesh_optimization_candidate.output_file_id"
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return names, nil
}
func (s *LibraryStore) ActiveFileReferenceIDs(ctx context.Context, ids []string) (map[string]struct{}, error) {
	refs := map[string]struct{}{}
	for _, id := range ids {
		names, err := s.ActiveFileReferenceNames(ctx, id)
		if err != nil {
			return nil, err
		}
		if _, exists := s.Files[id]; !exists || len(names) > 0 {
			refs[id] = struct{}{}
		}
	}
	return refs, nil
}
func (s *LibraryStore) LockDerivativeDeletionAssets(ctx context.Context, id string) ([]mediaasset.Asset, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	ids := []string{}
	for _, d := range s.Derivatives[id] {
		if d.AssetID != nil && !strings.HasPrefix(d.Type, "FILE_DERIVATIVE_TYPE_FAVICON_") {
			ids = append(ids, *d.AssetID)
		}
	}
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	result := []mediaasset.Asset{}
	for _, id := range ids {
		a, err := s.MediaAssets.Asset(ctx, id)
		if errors.Is(err, mediaasset.ErrAssetNotFound) {
			return nil, domainerr.Precondition("file derivative references a missing public asset")
		}
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, nil
}
func (s *LibraryStore) LockSourceDeletionAssets(ctx context.Context, id string) ([]mediaasset.Asset, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := []mediaasset.Asset{}
	for assetID := range s.AssetIDs {
		a, err := s.MediaAssets.Asset(ctx, assetID)
		if err != nil {
			return nil, err
		}
		if a.SourceFileID != nil && *a.SourceFileID == id {
			result = append(result, a)
		}
	}
	slices.SortFunc(result, func(a, b mediaasset.Asset) int { return cmp.Compare(a.ID, b.ID) })
	return result, nil
}
func (s *LibraryStore) BoundDeletionAssetIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	bindings, err := s.MediaAssets.LockAssetBindings(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := map[string]bool{}
	for _, b := range bindings {
		result[b.AssetID] = true
	}
	return result, nil
}
func (s *LibraryStore) RequestDeletionAsset(ctx context.Context, id string) error {
	if s.Fail != nil {
		return s.Fail
	}
	return mediaasset.NewLifecycle(mutationtest.New[mediaasset.Store](s.MediaAssets), "").RequestPublicAssetDeletion(ctx, id)
}
func (s *LibraryStore) RequestDeletionFavicon(_ context.Context, id string) error {
	if s.Fail != nil {
		return s.Fail
	}
	s.FaviconDeletions = append(s.FaviconDeletions, id)
	return nil
}
func (s *LibraryStore) LockDeletionMeshCandidates(_ context.Context, id string) ([]domain.DeletionMeshCandidate, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := []domain.DeletionMeshCandidate{}
	for _, m := range s.MeshRecords {
		if m.Witness.SourceFileID == id && !m.Selected {
			result = append(result, domain.DeletionMeshCandidate{ID: m.Witness.CandidateID, Status: m.Status, OutputFileID: optionalString(m.Witness.OutputFileID)})
		}
	}
	return result, nil
}
func optionalString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
func (s *LibraryStore) CancelDeletionMeshCandidate(_ context.Context, id string, now, expires time.Time) error {
	if s.Fail != nil {
		return s.Fail
	}
	if m, ok := s.MeshRecords[id]; ok {
		m.Status = "cancelled"
		m.CancelledAt = &now
		m.ExpiresAt = expires
		m.UpdatedAt = now
		s.MeshRecords[id] = m
	}
	return nil
}
func (s *LibraryStore) DeleteDeletionMeshCandidate(_ context.Context, id string) error {
	if s.Fail != nil {
		return s.Fail
	}
	delete(s.MeshRecords, id)
	delete(s.ReadyMeshes, id)
	return nil
}
func (s *LibraryStore) MarkFileDeletion(_ context.Context, id string, now time.Time) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	r, ok := s.Files[id]
	if !ok || r.PendingDeletion {
		return false, nil
	}
	r.PendingDeletion = true
	r.DeleteRequestedAt = &now
	s.Files[id] = r
	return true, nil
}
func (s *LibraryStore) DeletionEventFile(_ context.Context, id string) (domain.File, error) {
	if s.Fail != nil {
		return domain.File{}, s.Fail
	}
	r, ok := s.Files[id]
	if !ok || !r.PendingDeletion {
		return domain.File{}, domain.ErrFileNotFound
	}
	f := deliveryFile(r)
	return domain.File{ID: id, Extension: f.Extension, MimeType: f.MimeType, DeleteRequestedAt: f.DeleteRequestedAt}, nil
}
func (s *LibraryStore) DeletionGenerationRefs(_ context.Context, id string) ([]domain.FileGenerationDeleteRef, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := []domain.FileGenerationDeleteRef{}
	for _, g := range s.Generations {
		if g.FileID == id {
			result = append(result, domain.FileGenerationDeleteRef{ID: g.ID, FileID: g.FileID, Kind: g.Kind, ObjectPrefix: g.ObjectPrefix})
		}
	}
	slices.SortFunc(result, func(a, b domain.FileGenerationDeleteRef) int { return cmp.Compare(a.ID, b.ID) })
	return result, nil
}
func (s *LibraryStore) EnqueueFileDeletion(_ context.Context, target domain.DeletionTarget) error {
	if s.EnqueueError != nil {
		return s.EnqueueError
	}
	if s.Fail != nil {
		return s.Fail
	}
	s.DeletionOutbox = append(s.DeletionOutbox, target)
	return nil
}
func (s *LibraryStore) DeletionAudit(writer domainaudit.Appender) domainaudit.Appender { return writer }
func (s *LibraryStore) AuditRecorder() domainaudit.Appender                            { return libraryAudit{s} }

type libraryAudit struct{ s *LibraryStore }

func (a libraryAudit) AppendDomainAuditInTransaction(_ context.Context, _ *gorm.DB, r telemetry.AuditRecord) error {
	if a.s.Fail != nil {
		return a.s.Fail
	}
	a.s.Audits = append(a.s.Audits, r)
	return nil
}

func (s *LibraryStore) LockFolderSubtree(_ context.Context, id string) ([]string, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	if _, ok := s.Folders[id]; !ok {
		return nil, domain.ErrFolderNotFound
	}
	set := map[string]bool{id: true}
	for changed := true; changed; {
		changed = false
		for _, f := range s.Folders {
			if f.ParentID != nil && set[*f.ParentID] && !set[f.ID] {
				set[f.ID] = true
				changed = true
			}
		}
	}
	ids := []string{}
	for id := range set {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}
func (s *LibraryStore) FolderDeletionFileIDs(_ context.Context, ids []string) ([]string, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	files := []string{}
	for id, r := range s.Files {
		if !r.PendingDeletion && r.Row.ParentID != nil && slices.Contains(ids, *r.Row.ParentID) {
			files = append(files, id)
		}
	}
	slices.Sort(files)
	return files, nil
}
func (s *LibraryStore) DetachDeletionFiles(_ context.Context, ids []string) error {
	if s.Fail != nil {
		return s.Fail
	}
	for _, id := range ids {
		if r, ok := s.Files[id]; ok {
			r.Row.ParentID = nil
			r.Row.UpdatedAt = time.Now().UTC()
			s.Files[id] = r
		}
	}
	return nil
}
func (s *LibraryStore) DeleteFolder(ctx context.Context, id string) error {
	if s.Fail != nil {
		return s.Fail
	}
	ids, err := s.LockFolderSubtree(ctx, id)
	if errors.Is(err, domain.ErrFolderNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, id := range ids {
		delete(s.Folders, id)
	}
	for id, r := range s.Files {
		if r.Row.ParentID != nil && slices.Contains(ids, *r.Row.ParentID) {
			r.Row.ParentID = nil
			s.Files[id] = r
		}
	}
	return nil
}
func (s *LibraryStore) FileUsages(_ context.Context, ids []string) ([]domain.FileUsageRow, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := []domain.FileUsageRow{}
	for _, r := range s.Usages {
		if slices.Contains(ids, r.FileID) {
			result = append(result, r)
		}
	}
	slices.SortFunc(result, func(a, b domain.FileUsageRow) int {
		key := func(r domain.FileUsageRow) string {
			block := ""
			if r.BlockID != nil {
				block = *r.BlockID
			}
			return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", r.FileID, r.Domain, r.EntityID, r.ReferencePath, block)
		}
		return cmp.Compare(key(a), key(b))
	})
	return result, nil
}
func (s *LibraryStore) FileUsageSlugs(_ context.Context, ids []string) ([]domain.FileUsageSlug, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := []domain.FileUsageSlug{}
	for _, r := range s.Slugs {
		if slices.Contains(ids, r.EntityID) {
			result = append(result, r)
		}
	}
	return result, nil
}
func (s *LibraryStore) TrackReleaseIDs(_ context.Context, ids []string) (map[string]string, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := map[string]string{}
	for _, id := range ids {
		if release, ok := s.TrackReleases[id]; ok {
			result[id] = release
		}
	}
	return result, nil
}
func (s *LibraryStore) ActiveFileCount(_ context.Context, id string) (int64, error) {
	if s.Fail != nil {
		return 0, s.Fail
	}
	if r, ok := s.Files[id]; ok && !r.PendingDeletion {
		return 1, nil
	}
	return 0, nil
}
