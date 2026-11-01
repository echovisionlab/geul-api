package mediaassettest

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
)

func (s *Store) DueRetiredGenerationIDs(_ context.Context, now time.Time) ([]string, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []mediaasset.Generation
	for _, value := range s.generations {
		if value.Status == mediaasset.GenerationRetired && value.DeleteAfter != nil && !value.DeleteAfter.After(now) {
			rows = append(rows, value)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].DeleteAfter.Equal(*rows[j].DeleteAfter) {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].DeleteAfter.Before(*rows[j].DeleteAfter)
	})
	ids := make([]string, 0, len(rows))
	for _, row := range rows[:min(len(rows), mediaasset.RetiredGenerationCleanupBatchSize)] {
		ids = append(ids, row.ID)
	}
	return ids, nil
}
func (s *Store) FileDeletionPending(_ context.Context, id string) (bool, error) {
	return s.files[id] != nil, s.Fail
}
func (s *Store) GenerationReferenced(_ context.Context, id string) (bool, error) {
	return s.GenerationReferences[id], s.Fail
}
func (s *Store) DeleteGeneration(_ context.Context, id string) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	_, found := s.generations[id]
	delete(s.generations, id)
	return found, nil
}
func (s *Store) ReferencedGenerationIDs(_ context.Context, ids []string) (map[string]struct{}, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	result := make(map[string]struct{})
	for _, id := range ids {
		if s.GenerationReferences[id] {
			result[id] = struct{}{}
		}
	}
	return result, nil
}
func (s *Store) PendingAssets(_ context.Context) ([]mediaasset.Asset, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []mediaasset.Asset
	for _, asset := range s.assets {
		if asset.Status == mediaasset.AssetDeletePending {
			rows = append(rows, cloneAsset(asset))
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.DeleteRequestedAt == nil || b.DeleteRequestedAt == nil || a.DeleteRequestedAt.Equal(*b.DeleteRequestedAt) {
			return a.ID < b.ID
		}
		return a.DeleteRequestedAt.Before(*b.DeleteRequestedAt)
	})
	return rows, nil
}
func (s *Store) AssetBindingCount(_ context.Context, id string) (int64, error) {
	if s.Fail != nil {
		return 0, s.Fail
	}
	var count int64
	for _, binding := range s.bindings {
		if binding.AssetID == id {
			count++
		}
	}
	return count, nil
}
func (s *Store) MarkAssetDeleted(_ context.Context, id string, now time.Time) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	asset, found := s.assets[id]
	if !found || asset.Status != mediaasset.AssetDeletePending {
		return false, nil
	}
	asset.Status = mediaasset.AssetDeleted
	asset.DeletedAt = &now
	asset.UpdatedAt = now
	s.PutAsset(asset)
	return true, nil
}
func (s *Store) ExpiredUnreadyAssetIDs(_ context.Context, cutoff time.Time) ([]string, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []mediaasset.Asset
	for _, asset := range s.assets {
		if (asset.Status == mediaasset.AssetAllocated || asset.Status == mediaasset.AssetFailed) && asset.CreatedAt.Before(cutoff) && !s.OGAssetReferences[asset.ID] {
			rows = append(rows, asset)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].CreatedAt.Before(rows[j].CreatedAt)
	})
	var ids []string
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}
func (s *Store) AssetHasGeneration(_ context.Context, id string) (bool, error) {
	return s.OGAssetReferences[id], s.Fail
}
func (s *Store) DeleteExpiredAsset(_ context.Context, id string, cutoff time.Time) (bool, error) {
	if s.Fail != nil {
		return false, s.Fail
	}
	asset, found := s.assets[id]
	if !found || (asset.Status != mediaasset.AssetAllocated && asset.Status != mediaasset.AssetFailed) || !asset.CreatedAt.Before(cutoff) {
		return false, nil
	}
	delete(s.assets, id)
	return true, nil
}
func (s *Store) LockVersionFiles(_ context.Context, ids []string) ([]mediaasset.VersionFile, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []mediaasset.VersionFile
	for id, pending := range s.files {
		if slices.Contains(ids, id) {
			rows = append(rows, mediaasset.VersionFile{ID: id, DeleteRequestedAt: copyPointer(pending)})
		}
	}
	return rows, nil
}
