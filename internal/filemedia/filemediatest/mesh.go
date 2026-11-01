package filemediatest

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

func (s *LibraryStore) PutMeshCandidate(row domain.MeshCandidate) {
	s.MeshCandidateRows[row.ID] = cloneMeshCandidate(row)
	record := MeshRecord{Witness: domain.MeshDeliveryWitness{CandidateID: row.ID, SourceFileID: row.SourceFileID}, Selected: row.SelectedAt != nil, Status: row.Status, CancelledAt: cloneUploadPointer(row.CancelledAt), UpdatedAt: row.UpdatedAt}
	if row.OutputFileID != nil {
		record.Witness.OutputFileID = *row.OutputFileID
	}
	if row.ExpiresAt != nil {
		record.ExpiresAt = *row.ExpiresAt
	}
	s.MeshRecords[row.ID] = record
	if row.Status == domain.MeshStatusReady && record.Witness.OutputFileID != "" {
		s.ReadyMeshes[row.ID] = record.Witness
	} else {
		delete(s.ReadyMeshes, row.ID)
	}
}
func (s *LibraryStore) MeshCandidates(_ context.Context, source, pipeline string) ([]domain.MeshCandidate, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	rows := []domain.MeshCandidate{}
	for _, row := range s.MeshCandidateRows {
		if row.SourceFileID == source && row.PipelineVersion == pipeline {
			rows = append(rows, cloneMeshCandidate(row))
		}
	}
	slices.SortFunc(rows, func(a, b domain.MeshCandidate) int {
		if n := cmp.Compare(a.TargetRatioPercent, b.TargetRatioPercent); n != 0 {
			return n
		}
		return b.CreatedAt.Compare(a.CreatedAt)
	})
	return rows, nil
}
func (s *LibraryStore) MeshCandidate(_ context.Context, id, job string, _ bool) (domain.MeshCandidate, error) {
	if s.Fail != nil {
		return domain.MeshCandidate{}, s.Fail
	}
	row, ok := s.MeshCandidateRows[id]
	if !ok || (job != "" && (row.JobID == nil || *row.JobID != job)) {
		return domain.MeshCandidate{}, domain.ErrMeshCandidateNotFound
	}
	return cloneMeshCandidate(row), nil
}
func (s *LibraryStore) MeshCandidateByCacheKey(_ context.Context, key string) (domain.MeshCandidate, error) {
	if s.Fail != nil {
		return domain.MeshCandidate{}, s.Fail
	}
	for _, row := range s.MeshCandidateRows {
		if row.CacheKey == key {
			return cloneMeshCandidate(row), nil
		}
	}
	return domain.MeshCandidate{}, domain.ErrMeshCandidateNotFound
}
func (s *LibraryStore) InsertMeshCandidate(_ context.Context, row domain.MeshCandidate) error {
	if s.Fail != nil {
		return s.Fail
	}
	if _, ok := s.MeshCandidateRows[row.ID]; ok {
		return errors.New("mesh candidate already exists")
	}
	for _, old := range s.MeshCandidateRows {
		if old.CacheKey == row.CacheKey {
			return errors.New("mesh cache key already exists")
		}
	}
	if _, ok := s.Files[row.SourceFileID]; !ok {
		return domain.ErrFileNotFound
	}
	s.PutMeshCandidate(row)
	return nil
}
func (s *LibraryStore) RequeueMeshCandidate(_ context.Context, id string, v domain.MeshRequeue) error {
	return s.updateMesh(id, func(row *domain.MeshCandidate) {
		row.Status = domain.MeshStatusPending
		row.OutputObjectID = v.OutputObjectID
		row.OutputFileID = nil
		row.JobID = new(v.JobID)
		row.ErrorMessage = nil
		row.SelectedAt = nil
		row.ExpiresAt = new(v.ExpiresAt)
		row.EnqueuedAt = new(v.At)
		row.ProcessingStartedAt = nil
		row.CompletedAt = nil
		row.FailedAt = nil
		row.CancelledAt = nil
		row.UpdatedAt = v.At
		if v.EntityType != nil {
			row.EntityType = cloneUploadPointer(v.EntityType)
		}
		if v.EntityID != nil {
			row.EntityID = cloneUploadPointer(v.EntityID)
		}
	})
}
func (s *LibraryStore) updateMesh(id string, apply func(*domain.MeshCandidate)) error {
	if s.Fail != nil {
		return s.Fail
	}
	if row, ok := s.MeshCandidateRows[id]; ok {
		apply(&row)
		s.PutMeshCandidate(row)
	}
	return nil
}
func (s *LibraryStore) MarkMeshProcessing(_ context.Context, id string, at, expires time.Time) error {
	return s.updateMesh(id, func(row *domain.MeshCandidate) {
		if row.Status == domain.MeshStatusPending {
			row.Status = domain.MeshStatusProcessing
			row.ProcessingStartedAt = new(at)
			row.ExpiresAt = new(expires)
			row.UpdatedAt = at
		}
	})
}
func (s *LibraryStore) SelectMeshCandidate(_ context.Context, id string, at time.Time) error {
	return s.updateMesh(id, func(row *domain.MeshCandidate) { row.SelectedAt = new(at); row.ExpiresAt = nil; row.UpdatedAt = at })
}
func (s *LibraryStore) CancelMeshCandidate(_ context.Context, id string, at, expires time.Time) error {
	return s.updateMesh(id, func(row *domain.MeshCandidate) {
		row.Status = domain.MeshStatusCancelled
		row.CancelledAt = new(at)
		row.ExpiresAt = new(expires)
		row.UpdatedAt = at
	})
}
func (s *LibraryStore) RetireMeshCandidate(_ context.Context, id string, at time.Time) error {
	return s.updateMesh(id, func(row *domain.MeshCandidate) {
		row.Status = domain.MeshStatusCancelled
		row.OutputFileID = nil
		row.CancelledAt = new(at)
		row.UpdatedAt = at
	})
}
func (s *LibraryStore) CompleteMeshCandidate(_ context.Context, id string, v domain.MeshCompletion) error {
	return s.updateMesh(id, func(row *domain.MeshCandidate) {
		row.OutputFileID = new(v.OutputFileID)
		row.OptimizedFileSize = cloneUploadPointer(v.OptimizedFileSize)
		row.OriginalFileSize = cloneUploadPointer(v.OriginalFileSize)
		row.OriginalVertexes = cloneUploadPointer(v.OriginalVertexes)
		row.OptimizedVertexes = cloneUploadPointer(v.OptimizedVertexes)
		row.OriginalTriangles = cloneUploadPointer(v.OriginalTriangles)
		row.OptimizedTriangles = cloneUploadPointer(v.OptimizedTriangles)
		row.ProcessingTimeMs = cloneUploadPointer(v.ProcessingTimeMS)
		row.ErrorMessage = nil
		row.CompletedAt = new(v.CompletedAt)
		row.ExpiresAt = new(v.ExpiresAt)
		row.UpdatedAt = v.CompletedAt
		row.Status = v.Status
		if v.Status == domain.MeshStatusReady {
			row.FailedAt = nil
			row.CancelledAt = nil
		} else {
			row.CancelledAt = new(v.CompletedAt)
		}
	})
}
func (s *LibraryStore) FailMeshCandidate(_ context.Context, id, message string, at, expires time.Time) error {
	return s.updateMesh(id, func(row *domain.MeshCandidate) {
		row.Status = domain.MeshStatusFailed
		row.ErrorMessage = new(message)
		row.FailedAt = new(at)
		row.ExpiresAt = new(expires)
		row.UpdatedAt = at
	})
}
func (s *LibraryStore) ExpiredMeshCandidates(_ context.Context, at time.Time) ([]domain.MeshCandidate, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	rows := []domain.MeshCandidate{}
	for _, row := range s.MeshCandidateRows {
		if row.SelectedAt == nil && row.ExpiresAt != nil && row.ExpiresAt.Before(at) {
			rows = append(rows, cloneMeshCandidate(row))
		}
	}
	return rows, nil
}
func (s *LibraryStore) DeleteMeshCandidate(_ context.Context, id string) error {
	if s.Fail != nil {
		return s.Fail
	}
	delete(s.MeshCandidateRows, id)
	delete(s.MeshRecords, id)
	delete(s.ReadyMeshes, id)
	return nil
}
func (s *LibraryStore) LockMeshCompletionFiles(_ context.Context, source, output string) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	row, ok := s.Files[source]
	if !ok {
		return false, domain.ErrFileNotFound
	}
	return !row.PendingDeletion, nil
}
func (s *LibraryStore) InsertMeshOutputFile(ctx context.Context, row domain.File) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	if _, ok := s.Files[row.ID]; ok {
		return false, nil
	}
	if err := s.InsertIngestFile(ctx, row); err != nil {
		return false, err
	}
	if !row.CreatedAt.IsZero() {
		record := s.Files[row.ID]
		record.Row.CreatedAt = row.CreatedAt
		s.Files[row.ID] = record
	}
	return true, nil
}
func (s *LibraryStore) MeshSourceFile(ctx context.Context, id string) (domain.File, error) {
	row, err := s.IngestFile(ctx, id)
	if err != nil {
		return row, err
	}
	if row.DeleteRequestedAt != nil {
		return domain.File{}, domain.ErrFileNotFound
	}
	return row, nil
}
func (s *LibraryStore) MeshSourceLinked(_ context.Context, kind, owner, file string) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	o, ok := s.ScopedOwners[kind+":"+owner]
	if !ok {
		return false, nil
	}
	for _, a := range s.ScopedAttachments {
		if a.OwnerKind == kind && a.OwnerID == owner && a.DocumentID == o.DocumentID && a.Attachment.FileID == file {
			return true, nil
		}
	}
	return false, nil
}
func (s *LibraryStore) RequireMeshPublisher(context.Context) error {
	if s.Fail != nil {
		return s.Fail
	}
	return s.MeshPublisherError
}
func (s *LibraryStore) EnqueueMeshJob(ctx context.Context, row domain.MeshJob) error {
	if s.MeshJobError != nil {
		return s.MeshJobError
	}
	if err := s.RequireMeshPublisher(ctx); err != nil {
		return err
	}
	if row.Source != nil {
		row.Source = new(*row.Source)
	}
	if row.Output != nil {
		row.Output = new(*row.Output)
	}
	row.SlotID = cloneUploadPointer(row.SlotID)
	row.AttemptID = cloneUploadPointer(row.AttemptID)
	s.MeshJobs = append(s.MeshJobs, row)
	return nil
}

func cloneMeshCandidate(row domain.MeshCandidate) domain.MeshCandidate {
	row.OutputFileID = cloneUploadPointer(row.OutputFileID)
	row.EntityType = cloneUploadPointer(row.EntityType)
	row.EntityID = cloneUploadPointer(row.EntityID)
	row.PublicAssetID = cloneUploadPointer(row.PublicAssetID)
	row.JobID = cloneUploadPointer(row.JobID)
	row.OriginalFileSize = cloneUploadPointer(row.OriginalFileSize)
	row.OptimizedFileSize = cloneUploadPointer(row.OptimizedFileSize)
	row.ProcessingTimeMs = cloneUploadPointer(row.ProcessingTimeMs)
	row.OriginalVertexes = cloneUploadPointer(row.OriginalVertexes)
	row.OptimizedVertexes = cloneUploadPointer(row.OptimizedVertexes)
	row.OriginalTriangles = cloneUploadPointer(row.OriginalTriangles)
	row.OptimizedTriangles = cloneUploadPointer(row.OptimizedTriangles)
	row.ErrorMessage = cloneUploadPointer(row.ErrorMessage)
	row.SelectedAt = cloneUploadPointer(row.SelectedAt)
	row.ExpiresAt = cloneUploadPointer(row.ExpiresAt)
	row.EnqueuedAt = cloneUploadPointer(row.EnqueuedAt)
	row.ProcessingStartedAt = cloneUploadPointer(row.ProcessingStartedAt)
	row.CompletedAt = cloneUploadPointer(row.CompletedAt)
	row.FailedAt = cloneUploadPointer(row.FailedAt)
	row.CancelledAt = cloneUploadPointer(row.CancelledAt)
	return row
}
