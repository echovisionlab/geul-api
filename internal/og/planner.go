package og

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

type preparedBulkOgRequest struct {
	request       Request
	entityType    string
	entityID      string
	locale        *string
	targetKind    string
	targetKey     string
	originalIndex int
}

// Keep each SQL statement comfortably below PostgreSQL's bind-parameter and
// expression-depth limits while retaining one transaction for the whole run.
const ogBulkWriteBatchSize = 250

// RequestBulkReloadedWithDB locks every target before reloading the canonical
// entity snapshots. This closes the window where a concurrent content/config
// mutation could otherwise leave an older snapshot as the latest generation.
func (p *Planner) RequestBulkReloadedWithDB(
	ctx context.Context,
	tx *gorm.DB,
	triggerKind string,
	reason string,
	requests []Request,
	reload ReloadRequests,
) (*Plan, error) {
	if reload == nil {
		return nil, errs.InvalidArgument("reload", "canonical OG request reloader is required")
	}
	if tx == nil {
		return nil, errs.InvalidArgument("transaction", "database transaction is required")
	}
	triggerKind = strings.TrimSpace(triggerKind)
	reason = strings.TrimSpace(reason)
	if triggerKind == "" {
		return nil, errs.Required("trigger_kind")
	}
	if reason == "" {
		return nil, errs.Required("reason")
	}
	if len(requests) == 0 {
		return nil, errs.InvalidArgument("targets", "at least one OG target is required")
	}
	plan := &Plan{RunID: uuid.NewString(), GenerationIDs: make([]string, len(requests))}
	if err := p.requestBulkWithDB(ctx, tx, plan, triggerKind, reason, requests, p.now().UTC(), reload); err != nil {
		return nil, err
	}
	return plan, nil
}

func (p *Planner) requestBulkWithDB(
	ctx context.Context,
	tx *gorm.DB,
	plan *Plan,
	triggerKind string,
	reason string,
	requests []Request,
	now time.Time,
	reload ReloadRequests,
) error {
	prepared, err := prepareBulkOgRequests(requests)
	if err != nil {
		return err
	}
	targets, err := lockBulkOgTargets(tx, prepared, now)
	if err != nil {
		return err
	}
	prepared, err = reloadCanonicalBulkOgRequests(ctx, tx, prepared, reload)
	if err != nil {
		return err
	}

	// The run-wide config snapshot is deliberately read after target locks. If
	// a settings transaction is waiting on one of these targets, its newer run
	// will follow this one; if it committed first, this read observes it.
	configJSON, revision, err := p.config.Snapshot(ctx, tx, p.cdnDomain)
	if err != nil {
		return fmt.Errorf("snapshot OG render config: %w", err)
	}
	run := model.OgGenerationRun{
		ID: plan.RunID, TriggerKind: triggerKind, Reason: reason,
		RenderConfigSnapshot: configJSON, ConfigRevision: revision,
		Status: model.OgGenerationRunStatusQueued, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(&run).Error; err != nil {
		return err
	}

	featuredAssets, err := loadBulkOgFeaturedAssets(ctx, tx, p.cdnDomain, prepared)
	if err != nil {
		return err
	}

	assets := make([]model.PublicAsset, len(prepared))
	generations := make([]model.OgGeneration, len(prepared))
	for i, item := range prepared {
		assetID := uuid.NewString()
		objectKey, err := mediaauth.AssetObjectKey(assetID, "webp")
		if err != nil {
			return err
		}
		assets[i] = model.PublicAsset{
			ID: assetID, Kind: "og", ObjectKey: objectKey, Extension: "webp", MimeType: "image/webp",
			Disposition: "inline", Status: model.PublicAssetStatusAllocated, CreatedAt: now, UpdatedAt: now,
		}
		target := targets[item.targetKey]
		snapshotJSON, err := json.Marshal(ogEntitySnapshot{
			EntityType: item.entityType, EntityID: item.entityID, Title: item.request.Title,
			Locale:        item.locale,
			FeaturedImage: featuredAssets[stringValue(item.request.FeaturedImageFileID)],
			Output:        ogOutputSnapshot{AssetID: assetID, ObjectKey: objectKey, Extension: "webp", MimeType: "image/webp"},
		})
		if err != nil {
			return err
		}
		generations[i] = model.OgGeneration{
			ID: assetID, RunID: run.ID, TargetID: target.ID,
			Status: model.OgGenerationStatusQueued, EntitySnapshot: snapshotJSON,
			DeadlineAt: now.Add(defaultOgGenerationDeadline),
			CreatedAt:  now, UpdatedAt: now,
		}
		plan.GenerationIDs[item.originalIndex] = assetID
	}
	if err := tx.CreateInBatches(&assets, ogBulkWriteBatchSize).Error; err != nil {
		return err
	}
	if err := tx.CreateInBatches(&generations, ogBulkWriteBatchSize).Error; err != nil {
		return err
	}

	for i, item := range prepared {
		target := targets[item.targetKey]
		if target.LatestGenerationID != nil && *target.LatestGenerationID != generations[i].ID {
			if err := supersedeActiveOgGeneration(
				tx,
				target.ID,
				*target.LatestGenerationID,
				generations[i].ID,
				now,
			); err != nil {
				return err
			}
		}
	}
	if err := updateBulkOgTargetPointers(tx, prepared, targets, generations, now); err != nil {
		return err
	}
	if err := p.releasePending(ctx, tx, prepared); err != nil {
		return err
	}

	for i, generation := range generations {
		target := targets[prepared[i].targetKey]
		target.LatestGenerationID = &generation.ID
		notifyOgLifecycle(tx, &generation, &target, model.OgGenerationStatusQueued, nil, nil, now)
	}
	if tx.Dialector.Name() == "postgres" {
		executor, ok := tx.Statement.ConnPool.(eventpkg.DBTX)
		if !ok {
			return fmt.Errorf("OG generation transaction does not expose a PGMQ executor")
		}
		for _, generation := range generations {
			if err := mq.EnqueueProtobuf(
				ctx,
				executor,
				eventpkg.QueueOgGenerate,
				generation.ID,
				&managev1.OgGenerationJob{GenerationId: generation.ID},
			); err != nil {
				return fmt.Errorf("enqueue OG generation %s: %w", generation.ID, err)
			}
		}
	}
	return nil
}

func reloadCanonicalBulkOgRequests(
	ctx context.Context,
	tx *gorm.DB,
	prepared []preparedBulkOgRequest,
	reload ReloadRequests,
) ([]preparedBulkOgRequest, error) {
	if reload == nil {
		return prepared, nil
	}
	reloadedRequests, err := reload(ctx, tx)
	if err != nil {
		return nil, err
	}
	reloaded, err := prepareBulkOgRequests(reloadedRequests)
	if err != nil {
		return nil, err
	}
	if len(reloaded) != len(prepared) {
		return nil, errs.FailedPrecondition("OG target set changed while taking its canonical snapshot")
	}

	reloadedByTarget := make(map[string]preparedBulkOgRequest, len(reloaded))
	for _, item := range reloaded {
		reloadedByTarget[item.targetKey] = item
	}
	canonical := make([]preparedBulkOgRequest, len(prepared))
	for i, original := range prepared {
		item, exists := reloadedByTarget[original.targetKey]
		if !exists {
			return nil, errs.FailedPrecondition("OG target set changed while taking its canonical snapshot")
		}
		item.originalIndex = original.originalIndex
		canonical[i] = item
	}
	return canonical, nil
}

// A pending render must not keep serving the previous raster because it can
// contain an obsolete title, logo, featured image, or legal notice. Clearing
// the projection and its exact binding makes public metadata use the entity
// fallback (then Site OG) until the latest generation completes.
func (p *Planner) releasePending(ctx context.Context, tx *gorm.DB, prepared []preparedBulkOgRequest) error {
	for _, item := range prepared {
		projection, err := projectionFor(p.projections, item.target())
		if err != nil {
			return err
		}
		if err := projection.ReleasePending(ctx, tx, item.target(), p.cdnDomain); err != nil {
			return err
		}
	}
	return nil
}

func prepareBulkOgRequests(requests []Request) ([]preparedBulkOgRequest, error) {
	prepared := make([]preparedBulkOgRequest, 0, len(requests))
	seen := make(map[string]struct{}, len(requests))
	for index, request := range requests {
		entityType := strings.TrimSpace(request.EntityType)
		if entityType == "" {
			return nil, errs.Required("entity_type")
		}
		entityID := strings.TrimSpace(request.EntityID)
		if entityID == "" {
			return nil, errs.Required("entity_id")
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
		case "entity":
			if locale != nil {
				return nil, errs.InvalidArgument("locale", "entity OG targets do not accept a locale")
			}
		case "locale":
			if locale == nil {
				return nil, errs.Required("locale")
			}
		default:
			return nil, errs.InvalidArgument("kind", "must be entity or locale")
		}
		key := bulkOgTargetKey(entityType, entityID, locale)
		if _, exists := seen[key]; exists {
			return nil, errs.InvalidArgument("targets", "duplicate OG target "+key)
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
	tx *gorm.DB,
	cdnDomain string,
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
	var assets []model.PublicAsset
	if err := tx.WithContext(ctx).
		Where("source_file_id IN ? AND status = ?", fileIDs, model.PublicAssetStatusReady).
		Order("source_file_id, created_at DESC").Find(&assets).Error; err != nil {
		return nil, err
	}
	lifecycle := mediaasset.NewLifecycle(tx, cdnDomain)
	for _, asset := range assets {
		fileID := stringValue(asset.SourceFileID)
		if _, exists := result[fileID]; exists {
			continue
		}
		ref, err := lifecycle.AssetRef(asset)
		if err != nil {
			return nil, err
		}
		result[fileID] = snapshotAssetRef(ref)
	}
	for _, fileID := range fileIDs {
		if result[fileID] == nil {
			return nil, errs.NotFound("public_asset_for_source_file", fileID)
		}
	}
	return result, nil
}

func lockBulkOgTargets(
	tx *gorm.DB,
	prepared []preparedBulkOgRequest,
	now time.Time,
) (map[string]model.OgGenerationTarget, error) {
	ordered := append([]preparedBulkOgRequest(nil), prepared...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].targetKey < ordered[j].targetKey })
	proposals := make([]model.OgGenerationTarget, len(ordered))
	for i, item := range ordered {
		proposals[i] = model.OgGenerationTarget{
			ID: uuid.NewString(), EntityType: item.entityType, EntityID: item.entityID,
			TargetKind: item.targetKind, Locale: item.locale, CreatedAt: now, UpdatedAt: now,
		}
	}
	conflict := clause.OnConflict{
		Columns: []clause.Column{
			{Name: "entity_type"},
			{Name: "entity_id"},
			{Name: "locale"},
		},
		DoNothing: true,
	}
	if tx.Dialector.Name() == "postgres" {
		conflict.Columns = nil
		conflict.OnConstraint = "uq_og_generation_target_identity"
	}
	if err := tx.Clauses(conflict).CreateInBatches(&proposals, ogBulkWriteBatchSize).Error; err != nil {
		return nil, err
	}
	var rows []model.OgGenerationTarget
	for start := 0; start < len(ordered); start += ogBulkWriteBatchSize {
		end := min(start+ogBulkWriteBatchSize, len(ordered))
		batch := ordered[start:end]
		where := make([]string, 0, len(batch))
		args := make(structured.Values, 0, len(batch)*3)
		for _, item := range batch {
			if item.locale == nil {
				where = append(where, "(entity_type = ? AND entity_id = ? AND locale IS NULL)")
				args = append(args, item.entityType, item.entityID)
			} else {
				where = append(where, "(entity_type = ? AND entity_id = ? AND locale = ?)")
				args = append(args, item.entityType, item.entityID, *item.locale)
			}
		}
		var batchRows []model.OgGenerationTarget
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(strings.Join(where, " OR "), args...).
			Order("entity_type, entity_id, COALESCE(locale, '')").Find(&batchRows).Error; err != nil {
			return nil, err
		}
		rows = append(rows, batchRows...)
	}
	result := make(map[string]model.OgGenerationTarget, len(rows))
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

func updateBulkOgTargetPointers(
	tx *gorm.DB,
	prepared []preparedBulkOgRequest,
	targets map[string]model.OgGenerationTarget,
	generations []model.OgGeneration,
	now time.Time,
) error {
	for start := 0; start < len(prepared); start += ogBulkWriteBatchSize {
		end := min(start+ogBulkWriteBatchSize, len(prepared))
		var caseSQL strings.Builder
		caseSQL.WriteString("CASE id")
		args := make(structured.Values, 0, (end-start)*2)
		ids := make([]string, 0, end-start)
		for index := start; index < end; index++ {
			target := targets[prepared[index].targetKey]
			caseSQL.WriteString(" WHEN ? THEN ?")
			args = append(args, target.ID, generations[index].ID)
			ids = append(ids, target.ID)
		}
		caseSQL.WriteString(" ELSE latest_generation_id END")
		result := tx.Model(&model.OgGenerationTarget{}).Where("id IN ?", ids).Updates(structured.Fields{
			"latest_generation_id": gorm.Expr(caseSQL.String(), args...), "updated_at": now,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(ids)) {
			return errs.FailedPrecondition("one or more OG target pointers changed during bulk enqueue")
		}
	}
	return nil
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
