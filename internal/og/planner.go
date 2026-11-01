package og

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
)

const defaultOgGenerationDeadline = 30 * time.Minute

// Reload rereads the canonical requests of a run after every target lock.
type Reload func(context.Context, Store) ([]Request, error)

type preparedBulkOgRequest struct {
	request       Request
	entityType    string
	entityID      string
	locale        *string
	targetKind    string
	targetKey     string
	originalIndex int
}

// RequestBulk plans one run for requests in store's transaction. It locks
// every target before reloading the canonical entity snapshots, which closes
// the window where a concurrent content or config mutation could otherwise
// leave an older snapshot as the latest generation.
func RequestBulk(
	ctx context.Context,
	store Store,
	now time.Time,
	triggerKind string,
	reason string,
	requests []Request,
	reload Reload,
) (*Plan, error) {
	if reload == nil {
		return nil, domainerr.InvalidField("reload", "canonical OG request reloader is required")
	}
	triggerKind = strings.TrimSpace(triggerKind)
	reason = strings.TrimSpace(reason)
	if triggerKind == "" {
		return nil, domainerr.Required("trigger_kind")
	}
	if reason == "" {
		return nil, domainerr.Required("reason")
	}
	if len(requests) == 0 {
		return nil, domainerr.InvalidField("targets", "at least one OG target is required")
	}
	plan := &Plan{RunID: uuid.NewString(), GenerationIDs: make([]string, len(requests))}
	if err := requestBulk(ctx, store, plan, triggerKind, reason, requests, now.UTC(), reload); err != nil {
		return nil, err
	}
	return plan, nil
}

// RequestCurrent plans automatic work for the selected targets of one entity
// after its owning mutation, in that mutation's transaction.
func RequestCurrent(
	ctx context.Context,
	store Store,
	now time.Time,
	entityType, entityID string,
	selection Selection,
	reason string,
) (*Plan, error) {
	requests, err := ResolveRequests(ctx, store, entityType, entityID, selection)
	if err != nil {
		return nil, err
	}
	return RequestBulk(ctx, store, now, "automatic", reason, requests, func(ctx context.Context, store Store) ([]Request, error) {
		return ResolveRequests(ctx, store, entityType, entityID, selection)
	})
}

// ResolveRequests routes one generation request to the entity's own source.
func ResolveRequests(ctx context.Context, store Store, entityType, entityID string, selection Selection) ([]Request, error) {
	policy, ok := PolicyForEntityName(entityType)
	if !ok {
		return nil, domainerr.InvalidEntityType(entityType)
	}
	if !SupportsNewGeneration(policy) {
		return nil, domainerr.Precondition(
			"release OG generation is disabled; the artwork public asset is used directly",
		)
	}
	requests, err := store.ResolveRequests(ctx, policy.Name, entityID, selection)
	if errors.Is(err, ErrEntityNotFound) {
		return nil, domainerr.ResourceNotFound(policy.Name, strings.TrimSpace(entityID))
	}
	return requests, err
}

// CollectRequests combines every source's current requests for global work.
func CollectRequests(ctx context.Context, store Store) ([]Request, error) {
	requests, err := store.CollectRequests(ctx)
	if err != nil {
		return nil, err
	}
	return dedupeRequests(requests), nil
}

func dedupeRequests(requests []Request) []Request {
	result := make([]Request, 0, len(requests))
	seen := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		key := strings.TrimSpace(request.EntityType) + "\x00" +
			strings.TrimSpace(request.EntityID) + "\x00" + stringValue(request.Locale)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, request)
	}
	return result
}

func requestBulk(
	ctx context.Context,
	store Store,
	plan *Plan,
	triggerKind string,
	reason string,
	requests []Request,
	now time.Time,
	reload Reload,
) error {
	prepared, err := prepareBulkOgRequests(requests)
	if err != nil {
		return err
	}
	targets, err := lockBulkOgTargets(ctx, store, prepared, now)
	if err != nil {
		return err
	}
	prepared, err = reloadCanonicalBulkOgRequests(ctx, store, prepared, reload)
	if err != nil {
		return err
	}

	// The run-wide config snapshot is deliberately read after target locks. If
	// a settings transaction is waiting on one of these targets, its newer run
	// will follow this one; if it committed first, this read observes it.
	configJSON, revision, err := store.RenderConfigSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("snapshot OG render config: %w", err)
	}
	if err := store.InsertRun(ctx, Run{
		ID: plan.RunID, TriggerKind: triggerKind, Reason: reason,
		RenderConfigSnapshot: configJSON, ConfigRevision: revision,
		Status: RunQueued, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return err
	}

	featuredAssets, err := loadBulkOgFeaturedAssets(ctx, store, prepared)
	if err != nil {
		return err
	}

	generations := make([]NewGeneration, len(prepared))
	for i, item := range prepared {
		assetID := uuid.NewString()
		objectKey, err := mediaauth.AssetObjectKey(assetID, "webp")
		if err != nil {
			return err
		}
		resolvedTitle, err := store.ResolvePublicText(ctx, item.request.Title)
		if err != nil {
			return err
		}
		output := Output{AssetID: assetID, ObjectKey: objectKey, Extension: "webp", MimeType: "image/webp"}
		snapshotJSON, err := json.Marshal(ogEntitySnapshot{
			EntityType: item.entityType, EntityID: item.entityID, Title: resolvedTitle,
			Locale:        item.locale,
			FeaturedImage: featuredAssets[stringValue(optionalString(item.request.FeaturedImageFileID))],
			Output:        ogOutputSnapshot(output),
		})
		if err != nil {
			return err
		}
		generations[i] = NewGeneration{
			Generation: Generation{
				ID: assetID, RunID: plan.RunID, TargetID: targets[item.targetKey].ID,
				Status: StatusQueued, EntitySnapshot: snapshotJSON,
				DeadlineAt: now.Add(defaultOgGenerationDeadline),
				CreatedAt:  now, UpdatedAt: now,
			},
			Output: output,
		}
		plan.GenerationIDs[item.originalIndex] = assetID
	}
	if err := store.InsertGenerations(ctx, generations); err != nil {
		return err
	}

	for i, item := range prepared {
		target := targets[item.targetKey]
		if target.LatestGenerationID != nil && *target.LatestGenerationID != generations[i].ID {
			if err := supersedeActiveOgGeneration(ctx, store, target.ID, *target.LatestGenerationID, generations[i].ID, now); err != nil {
				return err
			}
		}
	}
	pointers := make([]TargetPointer, len(prepared))
	for i, item := range prepared {
		pointers[i] = TargetPointer{TargetID: targets[item.targetKey].ID, GenerationID: generations[i].ID}
	}
	updated, err := store.PointTargets(ctx, pointers, now)
	if err != nil {
		return err
	}
	if updated != int64(len(pointers)) {
		return domainerr.Precondition("one or more OG target pointers changed during bulk enqueue")
	}
	// A pending render must not keep serving the previous raster because it
	// can contain an obsolete title, logo, featured image, or legal notice.
	// Clearing the projection and its exact binding makes public metadata use
	// the entity fallback (then Site OG) until the latest generation completes.
	for _, item := range prepared {
		if err := store.ReleasePendingProjection(ctx, item.target()); err != nil {
			return err
		}
	}

	for i, generation := range generations {
		target := targets[prepared[i].targetKey]
		target.LatestGenerationID = &generation.ID
		store.NotifyLifecycle(ctx, LifecycleEvent{
			Generation: generation.Generation, Target: target, Status: StatusQueued, OccurredAt: now,
		})
	}
	for _, generation := range generations {
		if err := store.EnqueueGeneration(ctx, generation.ID); err != nil {
			return fmt.Errorf("enqueue OG generation %s: %w", generation.ID, err)
		}
	}
	return nil
}

func reloadCanonicalBulkOgRequests(
	ctx context.Context,
	store Store,
	prepared []preparedBulkOgRequest,
	reload Reload,
) ([]preparedBulkOgRequest, error) {
	reloadedRequests, err := reload(ctx, store)
	if err != nil {
		return nil, err
	}
	reloaded, err := prepareBulkOgRequests(reloadedRequests)
	if err != nil {
		return nil, err
	}
	if len(reloaded) != len(prepared) {
		return nil, domainerr.Precondition("OG target set changed while taking its canonical snapshot")
	}

	reloadedByTarget := make(map[string]preparedBulkOgRequest, len(reloaded))
	for _, item := range reloaded {
		reloadedByTarget[item.targetKey] = item
	}
	canonical := make([]preparedBulkOgRequest, len(prepared))
	for i, original := range prepared {
		item, exists := reloadedByTarget[original.targetKey]
		if !exists {
			return nil, domainerr.Precondition("OG target set changed while taking its canonical snapshot")
		}
		item.originalIndex = original.originalIndex
		canonical[i] = item
	}
	return canonical, nil
}

func prepareBulkOgRequests(requests []Request) ([]preparedBulkOgRequest, error) {
	prepared := make([]preparedBulkOgRequest, 0, len(requests))
	seen := make(map[string]struct{}, len(requests))
	for index, request := range requests {
		entityType := strings.TrimSpace(request.EntityType)
		if entityType == "" {
			return nil, domainerr.Required("entity_type")
		}
		entityID := strings.TrimSpace(request.EntityID)
		if entityID == "" {
			return nil, domainerr.Required("entity_id")
		}
		rawLocale := optionalString(request.Locale)
		canonicalLocale, err := (Target{Locale: rawLocale}).CanonicalLocale()
		if err != nil {
			return nil, err
		}
		var locale *string
		if canonicalLocale != "" {
			locale = &canonicalLocale
		}
		targetKind := strings.TrimSpace(request.Kind)
		switch targetKind {
		case KindEntity:
			if locale != nil {
				return nil, domainerr.InvalidField("locale", "entity OG targets do not accept a locale")
			}
		case KindLocale:
			if locale == nil {
				return nil, domainerr.Required("locale")
			}
		default:
			return nil, domainerr.InvalidField("kind", "must be entity or locale")
		}
		key := bulkOgTargetKey(entityType, entityID, locale)
		if _, exists := seen[key]; exists {
			return nil, domainerr.InvalidField("targets", "duplicate OG target "+key)
		}
		seen[key] = struct{}{}
		prepared = append(prepared, preparedBulkOgRequest{
			request: request, entityType: entityType, entityID: entityID, locale: locale,
			targetKind: targetKind, targetKey: key, originalIndex: index,
		})
	}
	return prepared, nil
}

func loadBulkOgFeaturedAssets(
	ctx context.Context,
	store Store,
	prepared []preparedBulkOgRequest,
) (map[string]*ogAssetRefSnapshot, error) {
	fileIDs := make([]string, 0)
	wanted := make(map[string]struct{})
	for _, item := range prepared {
		if fileID := optionalString(item.request.FeaturedImageFileID); fileID != nil {
			if _, exists := wanted[*fileID]; !exists {
				wanted[*fileID] = struct{}{}
				fileIDs = append(fileIDs, *fileID)
			}
		}
	}
	result := make(map[string]*ogAssetRefSnapshot, len(fileIDs))
	if len(fileIDs) == 0 {
		return result, nil
	}
	refs, err := store.ReadyAssetsForFiles(ctx, fileIDs)
	if err != nil {
		return nil, err
	}
	for _, fileID := range fileIDs {
		ref, ok := refs[fileID]
		if !ok {
			return nil, domainerr.ResourceNotFound("public_asset_for_source_file", fileID)
		}
		result[fileID] = snapshotAssetRef(ref)
	}
	return result, nil
}

func lockBulkOgTargets(
	ctx context.Context,
	store Store,
	prepared []preparedBulkOgRequest,
	now time.Time,
) (map[string]GenerationTarget, error) {
	ordered := append([]preparedBulkOgRequest(nil), prepared...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].targetKey < ordered[j].targetKey })
	proposals := make([]GenerationTarget, len(ordered))
	for i, item := range ordered {
		proposals[i] = GenerationTarget{
			ID: uuid.NewString(), EntityType: item.entityType, EntityID: item.entityID,
			TargetKind: item.targetKind, Locale: item.locale, CreatedAt: now, UpdatedAt: now,
		}
	}
	rows, err := store.LockTargets(ctx, proposals)
	if err != nil {
		return nil, err
	}
	result := make(map[string]GenerationTarget, len(rows))
	for _, row := range rows {
		result[bulkOgTargetKey(row.EntityType, row.EntityID, row.Locale)] = row
	}
	for _, item := range prepared {
		target, exists := result[item.targetKey]
		if !exists || target.TargetKind != item.targetKind {
			return nil, fmt.Errorf("failed to lock canonical OG target %s", item.targetKey)
		}
	}
	return result, nil
}

// supersedeActiveOgGeneration retires the target's previous active
// generation in favor of the one just planned.
func supersedeActiveOgGeneration(
	ctx context.Context,
	store Store,
	targetID string,
	generationID string,
	replacementID string,
	now time.Time,
) error {
	superseded, err := store.SupersedeReplacedGeneration(ctx, targetID, generationID, replacementID, now)
	if err != nil || !superseded {
		return err
	}
	generation, err := store.Generation(ctx, generationID)
	if err != nil {
		return err
	}
	target, err := store.Target(ctx, targetID)
	if err != nil {
		return err
	}
	store.NotifyLifecycle(ctx, LifecycleEvent{
		Generation: generation, Target: target, Status: StatusSuperseded, ReplacementID: &replacementID, OccurredAt: now,
	})
	return refreshOgRunStatus(ctx, store, generation.RunID, now)
}

func bulkOgTargetKey(entityType string, entityID string, locale *string) string {
	return entityType + "\x00" + entityID + "\x00" + stringValue(locale)
}

func (item preparedBulkOgRequest) target() Target {
	return Target{
		EntityType: item.entityType,
		EntityID:   item.entityID,
		Locale:     item.locale,
		Kind:       item.targetKind,
	}
}
