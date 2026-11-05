// Package ogtest holds the in-memory OG Store and its Store contract.
package ogtest

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/og"
)

// CDNDomain is the origin the fake renders asset URLs with.
const CDNDomain = "https://cdn.example.com"

// Store is an in-memory og.Store. Its ports are plain fields a test sets:
// Sources answers ResolveRequests per entity type, All answers
// CollectRequests, and projections record the targets they released and
// completed.
type Store struct {
	mu sync.Mutex

	targets     map[string]og.GenerationTarget
	generations map[string]og.Generation
	runs        map[string]og.Run
	assets      map[string]og.Asset
	bindings    map[string]int
	references  map[string]bool
	pointers    map[string]string
	missing     map[string]bool
	sequence    int64

	notifications []og.LifecycleEvent
	enqueued      []string
	released      []og.Target
	reconciled    int

	// Sources answers ResolveRequests for an entity type.
	Sources map[string]func(entityID string, selection og.Selection) ([]og.Request, error)
	// All answers CollectRequests.
	All []og.Request
	// RenderConfig is the snapshot RenderConfigSnapshot returns.
	RenderConfig []byte
	// Err makes the named method fail with the error.
	Err map[string]error
}

// New returns an empty fake Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[og.Store]) {
	store := NewStore()
	return store, mutationtest.New[og.Store](store)
}

// NewStore returns an empty fake Store.
func NewStore() *Store {
	return &Store{
		targets: map[string]og.GenerationTarget{}, generations: map[string]og.Generation{},
		runs: map[string]og.Run{}, assets: map[string]og.Asset{}, bindings: map[string]int{},
		references: map[string]bool{}, pointers: map[string]string{}, missing: map[string]bool{},
		Sources:      map[string]func(string, og.Selection) ([]og.Request, error){},
		RenderConfig: []byte(`{"site_title":"","primary_color":"#b02d23"}`),
		Err:          map[string]error{},
	}
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	targets := mutationtest.ClonePersisted(s.targets)
	generations := mutationtest.ClonePersisted(s.generations)
	runs := mutationtest.ClonePersisted(s.runs)
	assets := mutationtest.ClonePersisted(s.assets)
	bindings := mutationtest.ClonePersisted(s.bindings)
	pointers := mutationtest.ClonePersisted(s.pointers)
	notifications := mutationtest.ClonePersisted(s.notifications)
	enqueued := mutationtest.ClonePersisted(s.enqueued)
	released := mutationtest.ClonePersisted(s.released)
	sequence := s.sequence
	return mutationtest.Checkpoint{
		State: map[string]any{
			"targets":       targets,
			"generations":   generations,
			"runs":          runs,
			"assets":        assets,
			"bindings":      bindings,
			"pointers":      pointers,
			"notifications": notifications,
			"enqueued":      enqueued,
			"released":      released,
		},
		Restore: func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.targets = targets
			s.generations = generations
			s.runs = runs
			s.assets = assets
			s.bindings = bindings
			s.pointers = pointers
			s.notifications = notifications
			s.enqueued = enqueued
			s.released = released
			s.sequence = sequence
		},
	}
}

func (s *Store) fail(method string) error {
	return s.Err[method]
}

// SeedAsset stores a public asset.
func (s *Store) SeedAsset(asset og.Asset) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assets[asset.ID] = asset
}

// SeedBinding binds an asset to an owner other than an OG projection.
func (s *Store) SeedBinding(assetID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings[assetID]++
}

// SeedReference makes an entity row point at assetID.
func (s *Store) SeedReference(assetID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.references[assetID] = true
}

// RemoveProjectionRow makes completion of target answer that its row is gone.
func (s *Store) RemoveProjectionRow(target og.Target) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.missing[projectionKey(target)] = true
}

// UpdateGeneration rewrites a stored generation, as a test fixture.
func (s *Store) UpdateGeneration(id string, change func(*og.Generation)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	generation := s.generations[id]
	change(&generation)
	s.generations[id] = generation
}

// StoredGeneration returns a stored generation.
func (s *Store) StoredGeneration(id string) (og.Generation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	generation, ok := s.generations[id]
	return generation, ok
}

// StoredRun returns a stored run.
func (s *Store) StoredRun(id string) (og.Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	return run, ok
}

// StoredAsset returns a stored public asset.
func (s *Store) StoredAsset(id string) (og.Asset, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, ok := s.assets[id]
	return asset, ok
}

// StoredTarget returns the stored target of an entity and locale.
func (s *Store) StoredTarget(entityType, entityID string, locale *string) (og.GenerationTarget, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, target := range s.targets {
		if target.EntityType == entityType && target.EntityID == entityID && equalLocale(target.Locale, locale) {
			return target, true
		}
	}
	return og.GenerationTarget{}, false
}

// ProjectionAsset returns the asset a target's projection points at.
func (s *Store) ProjectionAsset(target og.Target) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	assetID, ok := s.pointers[projectionKey(target)]
	return assetID, ok
}

// Notifications returns the lifecycle events published so far.
func (s *Store) Notifications() []og.LifecycleEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.notifications)
}

// Enqueued returns the generation jobs enqueued so far.
func (s *Store) Enqueued() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.enqueued)
}

// Released returns the targets whose projections were released.
func (s *Store) Released() []og.Target {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.released)
}

// Reconciled counts global reconciliations.
func (s *Store) Reconciled() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reconciled
}

func (s *Store) LockTargets(_ context.Context, proposals []og.GenerationTarget) ([]og.GenerationTarget, error) {
	if err := s.fail("LockTargets"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	locked := make([]og.GenerationTarget, 0, len(proposals))
	for _, proposal := range proposals {
		target, found := s.findTarget(proposal.EntityType, proposal.EntityID, proposal.Locale)
		if !found {
			target = proposal
			target.Locale = cloneString(proposal.Locale)
			s.targets[target.ID] = target
		}
		locked = append(locked, cloneTarget(target))
	}
	return locked, nil
}

func (s *Store) findTarget(entityType, entityID string, locale *string) (og.GenerationTarget, bool) {
	for _, target := range s.targets {
		if target.EntityType == entityType && target.EntityID == entityID && equalLocale(target.Locale, locale) {
			return target, true
		}
	}
	return og.GenerationTarget{}, false
}

func (s *Store) PointTargets(_ context.Context, pointers []og.TargetPointer, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var updated int64
	for _, pointer := range pointers {
		target, ok := s.targets[pointer.TargetID]
		if !ok {
			continue
		}
		target.LatestGenerationID = new(pointer.GenerationID)
		target.UpdatedAt = now
		s.targets[target.ID] = target
		updated++
	}
	return updated, nil
}

func (s *Store) ClaimLatestTarget(_ context.Context, targetID, generationID string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target, ok := s.targets[targetID]
	if !ok || target.LatestGenerationID == nil || *target.LatestGenerationID != generationID {
		return false, nil
	}
	target.UpdatedAt = now
	s.targets[targetID] = target
	return true, nil
}

func (s *Store) ClearTargetPointer(_ context.Context, targetID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if target, ok := s.targets[targetID]; ok {
		target.LatestGenerationID = nil
		target.UpdatedAt = now
		s.targets[targetID] = target
	}
	return nil
}

func (s *Store) LockEntityTargets(_ context.Context, entityType, entityID string) ([]og.GenerationTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var targets []og.GenerationTarget
	for _, target := range s.targets {
		if target.EntityType == entityType && target.EntityID == entityID {
			targets = append(targets, cloneTarget(target))
		}
	}
	slices.SortFunc(targets, func(a, b og.GenerationTarget) int {
		return cmp.Or(cmp.Compare(stringValue(a.Locale), stringValue(b.Locale)), cmp.Compare(a.ID, b.ID))
	})
	return targets, nil
}

func (s *Store) Target(_ context.Context, targetID string) (og.GenerationTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target, ok := s.targets[targetID]
	if !ok {
		return og.GenerationTarget{}, og.ErrTargetNotFound
	}
	return cloneTarget(target), nil
}

func (s *Store) FindTarget(_ context.Context, key og.TargetKey) (og.GenerationTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target, ok := s.findTarget(key.EntityType, key.EntityID, key.Locale)
	if !ok || target.TargetKind != key.Kind {
		return og.GenerationTarget{}, og.ErrTargetNotFound
	}
	return cloneTarget(target), nil
}

func (s *Store) LockGeneration(_ context.Context, generationID string) (og.Generation, og.GenerationTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	generation, ok := s.generations[generationID]
	if !ok {
		return og.Generation{}, og.GenerationTarget{}, og.ErrGenerationNotFound
	}
	target, ok := s.targets[generation.TargetID]
	if !ok {
		return og.Generation{}, og.GenerationTarget{}, og.ErrTargetNotFound
	}
	return cloneGeneration(generation), cloneTarget(target), nil
}

func (s *Store) Generation(_ context.Context, generationID string) (og.Generation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	generation, ok := s.generations[generationID]
	if !ok {
		return og.Generation{}, og.ErrGenerationNotFound
	}
	return cloneGeneration(generation), nil
}

func (s *Store) RunGenerations(_ context.Context, runID string) ([]og.Generation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var generations []og.Generation
	for _, generation := range s.generations {
		if generation.RunID == runID {
			generations = append(generations, cloneGeneration(generation))
		}
	}
	slices.SortFunc(generations, func(a, b og.Generation) int { return cmp.Compare(a.RequestSequence, b.RequestSequence) })
	return generations, nil
}

func (s *Store) InsertGenerations(_ context.Context, generations []og.NewGeneration) error {
	if err := s.fail("InsertGenerations"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, generation := range generations {
		if _, exists := s.assets[generation.Output.AssetID]; exists {
			return errors.New("duplicate public asset")
		}
		if _, exists := s.runs[generation.RunID]; !exists {
			return errors.New("generation run does not exist")
		}
		if _, exists := s.targets[generation.TargetID]; !exists {
			return errors.New("generation target does not exist")
		}
		s.assets[generation.Output.AssetID] = og.Asset{
			ID: generation.Output.AssetID, Kind: "og", Extension: generation.Output.Extension,
			MimeType: generation.Output.MimeType, Disposition: "inline", Status: og.AssetAllocated,
			CreatedAt: generation.CreatedAt,
		}
	}
	for _, generation := range generations {
		s.sequence++
		stored := cloneGeneration(generation.Generation)
		stored.RequestSequence = s.sequence
		s.generations[stored.ID] = stored
	}
	return nil
}

func (s *Store) LockActiveGenerations(_ context.Context, targetID string) ([]og.Generation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var generations []og.Generation
	for _, generation := range s.generations {
		if generation.TargetID == targetID && (generation.Status == og.StatusQueued || generation.Status == og.StatusProcessing) {
			generations = append(generations, cloneGeneration(generation))
		}
	}
	slices.SortFunc(generations, func(a, b og.Generation) int {
		return cmp.Or(cmp.Compare(a.RequestSequence, b.RequestSequence), cmp.Compare(a.ID, b.ID))
	})
	return generations, nil
}

func (s *Store) SupersedeReplacedGeneration(_ context.Context, targetID, generationID, replacementID string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	generation, ok := s.generations[generationID]
	if !ok || generation.TargetID != targetID || (generation.Status != og.StatusQueued && generation.Status != og.StatusProcessing) {
		return false, nil
	}
	generation.Status = og.StatusSuperseded
	generation.SupersededAt, generation.CompletedAt, generation.UpdatedAt = new(now), new(now), now
	generation.SupersededByID = new(replacementID)
	s.generations[generationID] = generation
	return true, nil
}

func (s *Store) StartGeneration(_ context.Context, generationID, leaseToken string, leaseExpiresAt, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	generation, ok := s.generations[generationID]
	if !ok {
		return nil
	}
	generation.Status = og.StatusProcessing
	generation.ProcessingAt, generation.LeaseToken, generation.LeaseExpiresAt = new(now), new(leaseToken), new(leaseExpiresAt)
	generation.LastErrorCode, generation.UpdatedAt = nil, now
	s.generations[generationID] = generation
	return nil
}

func (s *Store) MarkGenerationReady(_ context.Context, generationID, leaseToken string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	generation, ok := s.generations[generationID]
	if !ok || generation.Status != og.StatusProcessing || generation.LeaseToken == nil || *generation.LeaseToken != leaseToken {
		return false, nil
	}
	generation.Status = og.StatusReady
	generation.ReadyAt, generation.CompletedAt, generation.UpdatedAt = new(now), new(now), now
	s.generations[generationID] = generation
	return true, nil
}

func (s *Store) MarkGenerationFailed(_ context.Context, generationID string, from og.GenerationStatus, code string, now time.Time) (bool, error) {
	if err := s.fail("MarkGenerationFailed"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	generation, ok := s.generations[generationID]
	if !ok || generation.Status != from {
		return false, nil
	}
	generation.Status = og.StatusFailed
	generation.FailedAt, generation.CompletedAt, generation.UpdatedAt = new(now), new(now), now
	generation.LeaseToken, generation.LeaseExpiresAt, generation.LastErrorCode = nil, nil, new(code)
	s.generations[generationID] = generation
	return true, nil
}

func (s *Store) MarkGenerationSuperseded(_ context.Context, generationID string, replacementID *string, clearLease bool, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	generation, ok := s.generations[generationID]
	if !ok || (generation.Status != og.StatusQueued && generation.Status != og.StatusProcessing) {
		return false, nil
	}
	generation.Status = og.StatusSuperseded
	generation.SupersededAt, generation.CompletedAt, generation.UpdatedAt = new(now), new(now), now
	if clearLease {
		generation.LeaseToken, generation.LeaseExpiresAt = nil, nil
	}
	if replacementID != nil {
		generation.SupersededByID = cloneString(replacementID)
	}
	s.generations[generationID] = generation
	return true, nil
}

func (s *Store) MarkGenerationCancelled(_ context.Context, generationID string, from og.GenerationStatus, now time.Time) (bool, error) {
	if err := s.fail("MarkGenerationCancelled"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	generation, ok := s.generations[generationID]
	if !ok || generation.Status != from {
		return false, nil
	}
	generation.Status = og.StatusCancelled
	generation.CancelledAt, generation.CompletedAt, generation.UpdatedAt = new(now), new(now), now
	generation.LeaseToken, generation.LeaseExpiresAt = nil, nil
	s.generations[generationID] = generation
	return true, nil
}

func (s *Store) InsertRun(_ context.Context, run og.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.runs[run.ID]; exists {
		return errors.New("duplicate run")
	}
	run.RenderConfigSnapshot = slices.Clone(run.RenderConfigSnapshot)
	s.runs[run.ID] = run
	return nil
}

func (s *Store) Run(_ context.Context, runID string) (og.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok {
		return og.Run{}, og.ErrRunNotFound
	}
	run.RenderConfigSnapshot = slices.Clone(run.RenderConfigSnapshot)
	return run, nil
}

func (s *Store) StartRun(_ context.Context, runID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok || run.Status != og.RunQueued {
		return nil
	}
	run.Status, run.StartedAt, run.UpdatedAt = og.RunRunning, new(now), now
	s.runs[runID] = run
	return nil
}

func (s *Store) LockRun(_ context.Context, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[runID]; !ok {
		return og.ErrRunNotFound
	}
	return nil
}

func (s *Store) RunStatusCounts(_ context.Context, runID string) (map[og.GenerationStatus]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[og.GenerationStatus]int64{}
	for _, generation := range s.generations {
		if generation.RunID == runID {
			counts[generation.Status]++
		}
	}
	return counts, nil
}

func (s *Store) SettleRun(_ context.Context, runID string, status og.RunStatus, completedAt *time.Time, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok {
		return nil
	}
	run.Status, run.CompletedAt, run.UpdatedAt = status, cloneTime(completedAt), now
	if run.StartedAt == nil {
		run.StartedAt = new(run.CreatedAt)
	}
	s.runs[runID] = run
	return nil
}

func (s *Store) RenderConfigSnapshot(context.Context) ([]byte, string, error) {
	if err := s.fail("RenderConfigSnapshot"); err != nil {
		return nil, "", err
	}
	return slices.Clone(s.RenderConfig), Revision(s.RenderConfig), nil
}

func (s *Store) ResolvePublicText(_ context.Context, text string) (string, error) {
	return text, nil
}

func (s *Store) ReadyAssetsForFiles(_ context.Context, fileIDs []string) (map[string]og.AssetRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	refs := map[string]og.AssetRef{}
	newest := map[string]og.Asset{}
	for _, asset := range s.assets {
		if asset.SourceFileID == nil || asset.Status != og.AssetReady || !slices.Contains(fileIDs, *asset.SourceFileID) {
			continue
		}
		if current, ok := newest[*asset.SourceFileID]; !ok || asset.CreatedAt.After(current.CreatedAt) {
			newest[*asset.SourceFileID] = asset
		}
	}
	for fileID, asset := range newest {
		ref, err := assetRef(asset)
		if err != nil {
			return nil, err
		}
		refs[fileID] = ref
	}
	return refs, nil
}

func (s *Store) ResolveRequests(_ context.Context, entityType, entityID string, selection og.Selection) ([]og.Request, error) {
	source, ok := s.Sources[entityType]
	if !ok {
		return nil, domainerr.InvalidEntityType(entityType)
	}
	return source(entityID, selection)
}

func (s *Store) CollectRequests(context.Context) ([]og.Request, error) {
	if err := s.fail("CollectRequests"); err != nil {
		return nil, err
	}
	return slices.Clone(s.All), nil
}

func (s *Store) ReconcileBeforeGlobalGeneration(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconciled++
	return nil
}

func (s *Store) ReleasePendingProjection(_ context.Context, target og.Target) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := projectionKey(target)
	if assetID, ok := s.pointers[key]; ok {
		s.bindings[assetID]--
		if s.bindings[assetID] <= 0 {
			delete(s.bindings, assetID)
		}
		delete(s.pointers, key)
	}
	s.released = append(s.released, target)
	return nil
}

func (s *Store) CompleteProjection(_ context.Context, target og.Target, assetID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := projectionKey(target)
	if s.missing[key] {
		return og.ErrTranslationTargetMissing
	}
	if previous, ok := s.pointers[key]; ok && previous != assetID {
		s.bindings[previous]--
		if s.bindings[previous] <= 0 {
			delete(s.bindings, previous)
		}
	}
	if s.pointers[key] != assetID {
		s.bindings[assetID]++
	}
	s.pointers[key] = assetID
	return nil
}

func (s *Store) CompleteAsset(_ context.Context, written og.WrittenAsset) (og.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, ok := s.assets[written.AssetID]
	if !ok {
		return og.Asset{}, domainerr.ResourceNotFound("public_asset", written.AssetID)
	}
	switch asset.Status {
	case og.AssetReady:
		if asset.FileSize == nil || *asset.FileSize != written.FileSize || !slices.Equal(asset.SHA256, written.SHA256) {
			return og.Asset{}, domainerr.Precondition("public asset completion conflicts with ready metadata")
		}
	case og.AssetAllocated:
		asset.Status, asset.FileSize, asset.SHA256 = og.AssetReady, new(written.FileSize), slices.Clone(written.SHA256)
		asset.ReadyAt = new(time.Now().UTC())
		s.assets[asset.ID] = asset
	default:
		return og.Asset{}, domainerr.Precondition("public asset is not allocated")
	}
	return asset, nil
}

func (s *Store) AssetRef(asset og.Asset) (og.AssetRef, error) {
	return assetRef(asset)
}

func assetRef(asset og.Asset) (og.AssetRef, error) {
	if asset.Disposition != "inline" || asset.FileSize == nil {
		return og.AssetRef{}, domainerr.Precondition("attachment public assets cannot be emitted")
	}
	return og.AssetRef{
		AssetID: asset.ID, URL: CDNDomain + "/asset/" + asset.ID + "." + asset.Extension,
		Extension: asset.Extension, MimeType: asset.MimeType, FileSize: *asset.FileSize, SHA256: slices.Clone(asset.SHA256),
	}, nil
}

func (s *Store) Asset(_ context.Context, assetID string) (og.Asset, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, ok := s.assets[assetID]
	return asset, ok, nil
}

func (s *Store) UnboundReadyAssetIDs(_ context.Context, cutoff time.Time) ([]string, error) {
	if err := s.fail("UnboundReadyAssetIDs"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var assets []og.Asset
	for _, asset := range s.assets {
		if asset.Kind == "og" && asset.Status == og.AssetReady && asset.ReadyAt != nil && asset.ReadyAt.Before(cutoff) && s.bindings[asset.ID] == 0 {
			assets = append(assets, asset)
		}
	}
	slices.SortFunc(assets, func(a, b og.Asset) int { return cmp.Or(a.ReadyAt.Compare(*b.ReadyAt), cmp.Compare(a.ID, b.ID)) })
	return assetIDs(assets), nil
}

func (s *Store) ExpiredTerminalAssetIDs(_ context.Context, cutoff time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var assets []og.Asset
	for _, asset := range s.assets {
		generation, ok := s.generations[asset.ID]
		if !ok || asset.Kind != "og" || !asset.CreatedAt.Before(cutoff) ||
			(asset.Status != og.AssetAllocated && asset.Status != og.AssetFailed) {
			continue
		}
		if generation.Status == og.StatusFailed || generation.Status == og.StatusSuperseded || generation.Status == og.StatusCancelled {
			assets = append(assets, asset)
		}
	}
	slices.SortFunc(assets, func(a, b og.Asset) int { return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID)) })
	return assetIDs(assets), nil
}

func (s *Store) LockAsset(_ context.Context, assetID string) (og.Asset, bool, error) {
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

func (s *Store) AssetReferenced(_ context.Context, assetID string) (bool, error) {
	if err := s.fail("AssetReferenced"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.references[assetID] {
		return true, nil
	}
	return slices.Contains(slices.Collect(maps.Values(s.pointers)), assetID), nil
}

func (s *Store) MarkUnboundAssetDeletePending(_ context.Context, assetID string, cutoff, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, ok := s.assets[assetID]
	if !ok || asset.Kind != "og" || asset.Status != og.AssetReady || asset.ReadyAt == nil || !asset.ReadyAt.Before(cutoff) {
		return false, nil
	}
	asset.Status = og.AssetDeletePending
	s.assets[assetID] = asset
	return true, nil
}

func (s *Store) MarkTerminalAssetDeletePending(_ context.Context, assetID string, cutoff, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, ok := s.assets[assetID]
	if !ok || asset.Kind != "og" || !asset.CreatedAt.Before(cutoff) || (asset.Status != og.AssetAllocated && asset.Status != og.AssetFailed) {
		return false, nil
	}
	asset.Status = og.AssetDeletePending
	s.assets[assetID] = asset
	return true, nil
}

func (s *Store) NotifyLifecycle(_ context.Context, event og.LifecycleEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifications = append(s.notifications, event)
}

func (s *Store) EnqueueGeneration(_ context.Context, generationID string) error {
	if err := s.fail("EnqueueGeneration"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enqueued = append(s.enqueued, generationID)
	return nil
}

func projectionKey(target og.Target) string {
	return target.EntityType + "\x00" + target.EntityID + "\x00" + stringValue(target.Locale)
}

func assetIDs(assets []og.Asset) []string {
	ids := make([]string, len(assets))
	for i, asset := range assets {
		ids[i] = asset.ID
	}
	return ids
}

func equalLocale(a, b *string) bool {
	return (a == nil) == (b == nil) && stringValue(a) == stringValue(b)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	return new(*value)
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	return new(*value)
}

func cloneTarget(target og.GenerationTarget) og.GenerationTarget {
	target.Locale = cloneString(target.Locale)
	target.LatestGenerationID = cloneString(target.LatestGenerationID)
	return target
}

func cloneGeneration(generation og.Generation) og.Generation {
	generation.EntitySnapshot = slices.Clone(generation.EntitySnapshot)
	generation.LeaseToken = cloneString(generation.LeaseToken)
	generation.LastErrorCode = cloneString(generation.LastErrorCode)
	generation.SupersededByID = cloneString(generation.SupersededByID)
	return generation
}

var _ og.Store = (*Store)(nil)
