package filemediatest

import (
	"context"
	"slices"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
)

// DeletionStore models the File deletion queries without storage or authorization.
type DeletionStore struct {
	Files        map[string]filemedia.File
	References   map[string][]string
	Generations  map[string][]mediaasset.Generation
	Transcodes   map[string][2]int64
	MeshResults  map[string]int64
	BeforeDelete func() error
}

func NewDeletionStore() *DeletionStore {
	return &DeletionStore{Files: make(map[string]filemedia.File), References: make(map[string][]string), Generations: make(map[string][]mediaasset.Generation), Transcodes: make(map[string][2]int64), MeshResults: make(map[string]int64)}
}
func (s *DeletionStore) Savepoint() mutationtest.Checkpoint {
	files := mutationtest.ClonePersisted(s.Files)
	return mutationtest.Checkpoint{
		State: map[string]any{
			"Files": files,
		},
		Restore: func() {
			s.Files = files
		},
	}
}
func (s *DeletionStore) LockDeletionFile(_ context.Context, id string) (filemedia.File, error) {
	row, ok := s.Files[id]
	if !ok {
		return filemedia.File{}, filemedia.ErrFileNotFound
	}
	return filemedia.File{ID: row.ID, Extension: row.Extension, MimeType: row.MimeType, DeleteRequestedAt: row.DeleteRequestedAt}, nil
}
func (s *DeletionStore) ActiveFileReferenceNames(_ context.Context, id string) ([]string, error) {
	return slices.Clone(s.References[id]), nil
}
func (s *DeletionStore) PendingTranscodeResultCounts(_ context.Context, id string) (int64, int64, error) {
	v := s.Transcodes[id]
	return v[0], v[1], nil
}
func (s *DeletionStore) FileGenerations(_ context.Context, id string) ([]mediaasset.Generation, error) {
	result := slices.Clone(s.Generations[id])
	slices.SortFunc(result, func(a, b mediaasset.Generation) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return result, nil
}
func (s *DeletionStore) PendingMeshResultCount(_ context.Context, id string) (int64, error) {
	return s.MeshResults[id], nil
}
func (s *DeletionStore) DeleteRequestedFile(_ context.Context, id string) (bool, error) {
	if s.BeforeDelete != nil {
		if err := s.BeforeDelete(); err != nil {
			return false, err
		}
	}
	row, ok := s.Files[id]
	if !ok || row.DeleteRequestedAt == nil {
		return false, nil
	}
	delete(s.Files, id)
	return true, nil
}
func (s *DeletionStore) FileExists(_ context.Context, id string) (bool, error) {
	_, ok := s.Files[id]
	return ok, nil
}
