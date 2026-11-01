// Package favicontest holds the in-memory favicon Store.
package favicontest

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaasset/mediaassettest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// Store is an in-memory favicon.Store.
type Store struct {
	CDNDomain   string
	mu          sync.Mutex
	files       map[string]string
	assets      map[string]favicon.DerivativeAsset
	derivatives map[string]map[string]string
	bindings    map[string]int
	deletions   []string
}

// New returns an empty fake Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[favicon.Store]) {
	store := &Store{CDNDomain: "https://cdn.example.com",
		files: map[string]string{}, assets: map[string]favicon.DerivativeAsset{},
		derivatives: map[string]map[string]string{}, bindings: map[string]int{},
	}
	return store, mutationtest.New[favicon.Store](store)
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	assets, bindings, deletions := maps.Clone(s.assets), maps.Clone(s.bindings), len(s.deletions)
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.assets, s.bindings, s.deletions = assets, bindings, s.deletions[:deletions]
	}
}

// SeedFile stores a source File of mimeType.
func (s *Store) SeedFile(fileID, mimeType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[fileID] = mimeType
}

// SeedAsset stores a public asset; a non-empty derivativeType also makes it
// the File's derivative of that type.
func (s *Store) SeedAsset(fileID, derivativeType string, asset favicon.DerivativeAsset) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assets[asset.ID] = asset
	if derivativeType != "" {
		if s.derivatives[fileID] == nil {
			s.derivatives[fileID] = map[string]string{}
		}
		s.derivatives[fileID][derivativeType] = asset.ID
	}
}

// SeedBinding binds an asset to an owner.
func (s *Store) SeedBinding(assetID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings[assetID]++
}

// Status returns a stored asset's status.
func (s *Store) Status(assetID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.assets[assetID].Status
}

// Deletions returns the assets whose deletion was requested.
func (s *Store) Deletions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.deletions)
}

func (s *Store) SourceMimeType(_ context.Context, fileID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mimeType, ok := s.files[fileID]
	return mimeType, ok, nil
}

func (s *Store) FaviconDerivatives(_ context.Context, fileID string) ([]favicon.DerivativeAsset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var assets []favicon.DerivativeAsset
	for derivativeType, assetID := range s.derivatives[fileID] {
		asset := s.assets[assetID]
		asset.Type = derivativeType
		assets = append(assets, asset)
	}
	slices.SortFunc(assets, func(a, b favicon.DerivativeAsset) int { return cmp.Compare(a.Type, b.Type) })
	return assets, nil
}

func (s *Store) NewestReadySourceAsset(_ context.Context, fileID string) (favicon.DerivativeAsset, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var newest favicon.DerivativeAsset
	found := false
	for _, asset := range s.assets {
		if asset.SourceFileID != nil && *asset.SourceFileID == fileID && asset.Kind == "favicon" && asset.Status == favicon.AssetReady &&
			(!found || asset.CreatedAt.After(newest.CreatedAt)) {
			newest, found = asset, true
		}
	}
	return newest, found, nil
}

func (s *Store) AssetRef(asset favicon.DerivativeAsset) (*mediaasset.Reference, error) {
	_, units := mediaassettest.New()
	return mediaasset.NewLifecycle(units, s.CDNDomain).AssetRef(mediaasset.Asset{
		ID: asset.ID, SourceFileID: asset.SourceFileID, Kind: asset.Kind, ObjectKey: asset.ObjectKey,
		Extension: asset.Extension, MimeType: asset.MimeType, FileSize: asset.FileSize, SHA256: asset.SHA256,
		Disposition: asset.Disposition, DownloadFilename: asset.DownloadFilename, Status: mediaasset.AssetStatus(asset.Status),
	})
}

func (s *Store) FaviconDerivativeCount(_ context.Context, fileID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.derivatives[fileID])), nil
}

func (s *Store) SourceAssetCount(_ context.Context, fileID string) (int64, error) {
	return int64(len(s.sourceAssets(fileID))), nil
}

func (s *Store) sourceAssets(fileID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, asset := range s.assets {
		if asset.SourceFileID != nil && *asset.SourceFileID == fileID && asset.Kind == "favicon" {
			ids = append(ids, asset.ID)
		}
	}
	return ids
}

func (s *Store) LockDeletionAssets(_ context.Context, fileID string) ([]favicon.DerivativeAsset, error) {
	ids := s.sourceAssets(fileID)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, assetID := range s.derivatives[fileID] {
		if !slices.Contains(ids, assetID) {
			ids = append(ids, assetID)
		}
	}
	slices.Sort(ids)
	assets := make([]favicon.DerivativeAsset, len(ids))
	for i, id := range ids {
		assets[i] = s.assets[id]
	}
	return assets, nil
}

func (s *Store) BindingCount(_ context.Context, assetIDs []string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, id := range assetIDs {
		count += int64(s.bindings[id])
	}
	return count, nil
}

func (s *Store) RequestAssetDeletion(_ context.Context, assetID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset := s.assets[assetID]
	asset.Status = favicon.AssetDeletePending
	s.assets[assetID] = asset
	s.deletions = append(s.deletions, assetID)
	return nil
}

func (s *Store) DanglingReadyAssetIDs(_ context.Context, cutoff time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, asset := range s.assets {
		if asset.Kind == "favicon" && asset.Status == favicon.AssetReady && asset.CreatedAt.Before(cutoff) && s.bindings[asset.ID] == 0 {
			ids = append(ids, asset.ID)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func (s *Store) LockAsset(_ context.Context, assetID string) (favicon.DerivativeAsset, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, ok := s.assets[assetID]
	return asset, ok, nil
}

func (s *Store) AssetBound(_ context.Context, assetID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bindings[assetID] != 0, nil
}

func (s *Store) MarkAssetDeletePending(_ context.Context, assetID string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, ok := s.assets[assetID]
	if !ok || asset.Status != favicon.AssetReady {
		return false, nil
	}
	asset.Status = favicon.AssetDeletePending
	s.assets[assetID] = asset
	return true, nil
}

var _ favicon.Store = (*Store)(nil)

func (s *Store) AttachSourceAsset(_ context.Context, id, file string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.assets[id]
	if !ok || a.Kind != "favicon" || a.Status != favicon.AssetReady || a.SourceFileID != nil {
		return false, nil
	}
	a.SourceFileID = new(file)
	s.assets[id] = a
	return true, nil
}

func (s *Store) ScheduleStagedDeletion(_ context.Context, ids []string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		a, ok := s.assets[id]
		if ok && (a.Status == favicon.AssetReady || a.Status == favicon.AssetFailed) {
			a.Status = favicon.AssetDeletePending
			s.assets[id] = a
		}
	}
	return nil
}
