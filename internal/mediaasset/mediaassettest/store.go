// Package mediaassettest supplies the in-memory Store and its shared contract.
package mediaassettest

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
)

type Store struct {
	GenerationReferences map[string]bool
	OGAssetReferences    map[string]bool
	assets               map[string]mediaasset.Asset
	bindings             map[bindingKey]mediaasset.AssetBinding
	generations          map[string]mediaasset.Generation
	files                map[string]*time.Time
	// Fail supplies a controlled storage failure without replacing the use case.
	Fail error
}
type bindingKey struct{ ownerType, ownerID, key string }

func New() (*Store, *mutationtest.UnitOfWork[mediaasset.Store]) {
	store := &Store{GenerationReferences: map[string]bool{}, OGAssetReferences: map[string]bool{}, assets: map[string]mediaasset.Asset{}, bindings: map[bindingKey]mediaasset.AssetBinding{}, generations: map[string]mediaasset.Generation{}, files: map[string]*time.Time{}}
	return store, mutationtest.New[mediaasset.Store](store)
}
func (s *Store) PutFile(id string, pending *time.Time) { s.files[id] = copyPointer(pending) }
func (s *Store) PutAsset(asset mediaasset.Asset)       { s.assets[asset.ID] = cloneAsset(asset) }
func (s *Store) PutGeneration(generation mediaasset.Generation) {
	s.generations[generation.ID] = cloneGeneration(generation)
}
func (s *Store) Savepoint() func() {
	assets := make(map[string]mediaasset.Asset, len(s.assets))
	for id, asset := range s.assets {
		assets[id] = cloneAsset(asset)
	}
	bindings := make(map[bindingKey]mediaasset.AssetBinding, len(s.bindings))
	for key, binding := range s.bindings {
		bindings[key] = cloneBinding(binding)
	}
	generations := make(map[string]mediaasset.Generation, len(s.generations))
	for id, generation := range s.generations {
		generations[id] = cloneGeneration(generation)
	}
	files := maps.Clone(s.files)
	generationReferences := maps.Clone(s.GenerationReferences)
	ogAssetReferences := maps.Clone(s.OGAssetReferences)
	return func() {
		s.assets = assets
		s.bindings = bindings
		s.generations = generations
		s.files = files
		s.GenerationReferences = generationReferences
		s.OGAssetReferences = ogAssetReferences
	}
}
func (s *Store) LockAttachableFiles(_ context.Context, ids []string) error {
	if s.Fail != nil {
		return s.Fail
	}
	ids = sliceutil.SortedUniqueTrimmed(ids)
	for _, id := range ids {
		if _, exists := s.files[id]; !exists {
			return domainerr.Precondition("one or more files no longer exist")
		}
	}
	for _, id := range ids {
		if s.files[id] != nil {
			return domainerr.Precondition("file is pending deletion")
		}
	}
	return nil
}
func (s *Store) InsertAsset(_ context.Context, asset mediaasset.Asset) error {
	if s.Fail != nil {
		return s.Fail
	}
	if _, exists := s.assets[asset.ID]; exists {
		return errors.New("asset already exists")
	}
	for _, other := range s.assets {
		if other.ObjectKey == asset.ObjectKey {
			return errors.New("asset object key already exists")
		}
	}
	s.PutAsset(asset)
	return nil
}
func (s *Store) Asset(_ context.Context, id string) (mediaasset.Asset, error) {
	if s.Fail != nil {
		return mediaasset.Asset{}, s.Fail
	}
	asset, exists := s.assets[id]
	if !exists {
		return mediaasset.Asset{}, mediaasset.ErrAssetNotFound
	}
	return cloneAsset(asset), nil
}
func (s *Store) LockAsset(ctx context.Context, id string) (mediaasset.Asset, error) {
	return s.Asset(ctx, id)
}
func (s *Store) LockAssets(_ context.Context, ids []string) (map[string]mediaasset.Asset, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := make(map[string]mediaasset.Asset)
	for _, id := range sliceutil.SortedUniqueTrimmed(ids) {
		if asset, exists := s.assets[id]; exists {
			result[id] = cloneAsset(asset)
		}
	}
	return result, nil
}
func (s *Store) ReadyAssetForFile(ctx context.Context, id, kind string) (mediaasset.Asset, error) {
	rows, err := s.ReadyAssetsForFiles(ctx, kind, []string{strings.TrimSpace(id)})
	if err != nil {
		return mediaasset.Asset{}, err
	}
	if len(rows) == 0 {
		return mediaasset.Asset{}, mediaasset.ErrAssetNotFound
	}
	return rows[0], nil
}
func (s *Store) ReadyAssetsForFiles(_ context.Context, kind string, ids []string) ([]mediaasset.Asset, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []mediaasset.Asset
	for _, asset := range s.assets {
		if asset.SourceFileID != nil && slices.Contains(ids, *asset.SourceFileID) && asset.Kind == kind && asset.Status == mediaasset.AssetReady {
			rows = append(rows, cloneAsset(asset))
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if *a.SourceFileID != *b.SourceFileID {
			return *a.SourceFileID < *b.SourceFileID
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID > b.ID
	})
	return rows, nil
}
func (s *Store) CompleteAsset(_ context.Context, id string, size int64, digest []byte, now time.Time) error {
	if s.Fail != nil {
		return s.Fail
	}
	asset, exists := s.assets[id]
	if !exists || asset.Status != mediaasset.AssetAllocated {
		return nil
	}
	asset.FileSize = &size
	asset.SHA256 = slices.Clone(digest)
	asset.Status = mediaasset.AssetReady
	asset.ReadyAt = &now
	asset.FailureReason = nil
	asset.FailedAt = nil
	asset.UpdatedAt = now
	s.PutAsset(asset)
	return nil
}
func (s *Store) Binding(_ context.Context, ownerType, ownerID, key string) (mediaasset.AssetBinding, bool, error) {
	if s.Fail != nil {
		return mediaasset.AssetBinding{}, false, s.Fail
	}
	binding, exists := s.bindings[bindingKey{ownerType, ownerID, key}]
	return cloneBinding(binding), exists, nil
}
func (s *Store) LockBinding(ctx context.Context, ownerType, ownerID, key string) (mediaasset.AssetBinding, bool, error) {
	return s.Binding(ctx, ownerType, ownerID, key)
}
func (s *Store) InsertBinding(_ context.Context, binding mediaasset.AssetBinding) error {
	if s.Fail != nil {
		return s.Fail
	}
	key := bindingKey{binding.OwnerType, binding.OwnerID, binding.BindingKey}
	if _, exists := s.bindings[key]; exists {
		return mediaasset.ErrBindingConflict
	}
	if _, exists := s.assets[binding.AssetID]; !exists {
		return errors.New("asset does not exist")
	}
	s.bindings[key] = cloneBinding(binding)
	return nil
}
func (s *Store) ReplaceBinding(_ context.Context, binding mediaasset.AssetBinding, expected string) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	key := bindingKey{binding.OwnerType, binding.OwnerID, binding.BindingKey}
	current, exists := s.bindings[key]
	if !exists || current.AssetID != expected {
		return false, nil
	}
	current.AssetID = binding.AssetID
	current.SourceFileID = copyPointer(binding.SourceFileID)
	current.UpdatedAt = binding.UpdatedAt
	s.bindings[key] = current
	return true, nil
}
func (s *Store) BindingKeys(_ context.Context, ownerType, ownerID, prefix string) ([]string, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var keys []string
	for key := range s.bindings {
		if key.ownerType == ownerType && key.ownerID == ownerID && (key.key == prefix || strings.HasPrefix(key.key, prefix+"_")) {
			keys = append(keys, key.key)
		}
	}
	slices.Sort(keys)
	return keys, nil
}
func (s *Store) Bindings(_ context.Context, ownerType, ownerID string, keys []string) ([]mediaasset.AssetBinding, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []mediaasset.AssetBinding
	for key, binding := range s.bindings {
		if key.ownerType == ownerType && key.ownerID == ownerID && slices.Contains(keys, key.key) {
			rows = append(rows, cloneBinding(binding))
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].BindingKey < rows[j].BindingKey })
	return rows, nil
}
func (s *Store) LockBindings(ctx context.Context, ownerType, ownerID string, keys []string) ([]mediaasset.AssetBinding, error) {
	return s.Bindings(ctx, ownerType, ownerID, keys)
}
func (s *Store) LockAssetBindings(_ context.Context, ids []string) ([]mediaasset.AssetBinding, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []mediaasset.AssetBinding
	for _, binding := range s.bindings {
		if slices.Contains(ids, binding.AssetID) {
			rows = append(rows, cloneBinding(binding))
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].AssetID < rows[j].AssetID })
	return rows, nil
}
func (s *Store) DeleteBindings(_ context.Context, ownerType, ownerID string, keys []string) (int64, error) {
	if s.Fail != nil {
		return 0, s.Fail
	}
	var count int64
	for key := range s.bindings {
		if key.ownerType == ownerType && key.ownerID == ownerID && slices.Contains(keys, key.key) {
			delete(s.bindings, key)
			count++
		}
	}
	return count, nil
}
func (s *Store) MarkUnboundAssetsDeletePending(_ context.Context, ids []string, now time.Time) error {
	if s.Fail != nil {
		return s.Fail
	}
	for _, id := range ids {
		asset, exists := s.assets[id]
		if !exists || asset.Status != mediaasset.AssetReady || asset.SourceFileID != nil {
			continue
		}
		bound := false
		for _, binding := range s.bindings {
			if binding.AssetID == id {
				bound = true
				break
			}
		}
		if !bound {
			asset.Status = mediaasset.AssetDeletePending
			asset.DeleteRequestedAt = &now
			asset.UpdatedAt = now
			s.PutAsset(asset)
		}
	}
	return nil
}
func (s *Store) MarkAssetDeletePending(_ context.Context, id string, now time.Time) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	asset, exists := s.assets[id]
	if !exists || (asset.Status != mediaasset.AssetReady && asset.Status != mediaasset.AssetFailed) {
		return false, nil
	}
	asset.Status = mediaasset.AssetDeletePending
	asset.DeleteRequestedAt = &now
	asset.UpdatedAt = now
	s.PutAsset(asset)
	return true, nil
}
func (s *Store) Generation(_ context.Context, id string) (mediaasset.Generation, error) {
	if s.Fail != nil {
		return mediaasset.Generation{}, s.Fail
	}
	value, exists := s.generations[id]
	if !exists {
		return mediaasset.Generation{}, mediaasset.ErrGenerationNotFound
	}
	return cloneGeneration(value), nil
}
func (s *Store) LockGeneration(ctx context.Context, id string) (mediaasset.Generation, error) {
	return s.Generation(ctx, id)
}
func (s *Store) RetireOtherGenerations(_ context.Context, fileID, id string, now, deleteAfter time.Time) error {
	if s.Fail != nil {
		return s.Fail
	}
	for key, value := range s.generations {
		if value.FileID == fileID && value.ID != id && value.Status == mediaasset.GenerationReady {
			value.Status = mediaasset.GenerationRetired
			value.RetiredAt = &now
			value.DeleteAfter = &deleteAfter
			value.UpdatedAt = now
			s.generations[key] = cloneGeneration(value)
		}
	}
	return nil
}
func (s *Store) CompleteGeneration(_ context.Context, id string, digest []byte, count int32, size int64, now time.Time) error {
	if s.Fail != nil {
		return s.Fail
	}
	value, exists := s.generations[id]
	if !exists || value.Status != mediaasset.GenerationAllocated {
		return nil
	}
	value.ManifestSHA256 = slices.Clone(digest)
	value.ObjectCount = &count
	value.TotalSize = &size
	value.Status = mediaasset.GenerationReady
	value.ReadyAt = &now
	value.UpdatedAt = now
	s.PutGeneration(value)
	return nil
}
func copyPointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	value := *p
	return &value
}
func cloneAsset(value mediaasset.Asset) mediaasset.Asset {
	value.SourceFileID = copyPointer(value.SourceFileID)
	value.FileSize = copyPointer(value.FileSize)
	value.SHA256 = slices.Clone(value.SHA256)
	value.DownloadFilename = copyPointer(value.DownloadFilename)
	value.ReadyAt = copyPointer(value.ReadyAt)
	value.DeleteRequestedAt = copyPointer(value.DeleteRequestedAt)
	value.DeletedAt = copyPointer(value.DeletedAt)
	value.FailedAt = copyPointer(value.FailedAt)
	value.FailureReason = copyPointer(value.FailureReason)
	return value
}
func cloneBinding(value mediaasset.AssetBinding) mediaasset.AssetBinding {
	value.SourceFileID = copyPointer(value.SourceFileID)
	return value
}
func cloneGeneration(value mediaasset.Generation) mediaasset.Generation {
	value.ManifestSHA256 = slices.Clone(value.ManifestSHA256)
	value.ObjectCount = copyPointer(value.ObjectCount)
	value.TotalSize = copyPointer(value.TotalSize)
	value.ReadyAt = copyPointer(value.ReadyAt)
	value.RetiredAt = copyPointer(value.RetiredAt)
	value.DeleteAfter = copyPointer(value.DeleteAfter)
	return value
}

func (s *Store) LockAssetForDeletion(ctx context.Context, id string) (mediaasset.Asset, error) {
	return s.Asset(ctx, id)
}
func (s *Store) LockExpiredAsset(ctx context.Context, id string) (mediaasset.Asset, error) {
	return s.Asset(ctx, id)
}
func (s *Store) LockRetiredGeneration(ctx context.Context, id string) (mediaasset.Generation, error) {
	return s.Generation(ctx, id)
}
