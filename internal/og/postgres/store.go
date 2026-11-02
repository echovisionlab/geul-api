// Package ogpostgres is the PostgreSQL store of OG generation and the
// gorm-facing ports other domains plug into it.
package ogpostgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// Keep each SQL statement comfortably below PostgreSQL's bind-parameter and
// expression-depth limits while retaining one transaction for the whole run.
const ogBulkWriteBatchSize = 250

// Ports are the other domains the store reaches inside OG's transaction.
type Ports struct {
	// CDNDomain is the origin of public asset URLs.
	CDNDomain    string
	RenderConfig RenderConfig
	Projections  []Projection
	Requests     *Resolver
	AllRequests  *Collector
	Reconciler   GlobalReconciler
	References   []AssetReference
}

// NewUnits returns OG's unit of work over db.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[og.Store], error) {
	var missing []string
	for name, absent := range map[string]bool{
		"db": db == nil, "RenderConfig": ports.RenderConfig == nil, "Projections": ports.Projections == nil,
		"Requests": ports.Requests == nil, "AllRequests": ports.AllRequests == nil,
		"Reconciler": ports.Reconciler == nil, "References": ports.References == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return nil, fmt.Errorf("OG units: missing %s", strings.Join(missing, ", "))
	}
	ports.CDNDomain = strings.TrimRight(strings.TrimSpace(ports.CDNDomain), "/")
	return mutation.Postgres(db, func(tx *gorm.DB) og.Store { return NewStore(tx, ports) }), nil
}

// NewStore binds the OG store to tx, the transaction of the caller that joins
// it. A port NewStore leaves unset fails the call that needs it.
func NewStore(tx *gorm.DB, ports Ports) og.Store {
	return &store{tx: tx, ports: ports}
}

type store struct {
	tx    *gorm.DB
	ports Ports
}

func (s *store) LockTargets(ctx context.Context, proposals []og.GenerationTarget) ([]og.GenerationTarget, error) {
	tx := s.tx.WithContext(ctx)
	rows := make([]model.OgGenerationTarget, len(proposals))
	for i, proposal := range proposals {
		rows[i] = targetRow(proposal)
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
	if err := tx.Clauses(conflict).CreateInBatches(&rows, ogBulkWriteBatchSize).Error; err != nil {
		return nil, err
	}
	var locked []model.OgGenerationTarget
	for start := 0; start < len(proposals); start += ogBulkWriteBatchSize {
		end := min(start+ogBulkWriteBatchSize, len(proposals))
		batch := proposals[start:end]
		where := make([]string, 0, len(batch))
		args := make(structured.Values, 0, len(batch)*3)
		for _, item := range batch {
			if item.Locale == nil {
				where = append(where, "(entity_type = ? AND entity_id = ? AND locale IS NULL)")
				args = append(args, item.EntityType, item.EntityID)
			} else {
				where = append(where, "(entity_type = ? AND entity_id = ? AND locale = ?)")
				args = append(args, item.EntityType, item.EntityID, *item.Locale)
			}
		}
		var batchRows []model.OgGenerationTarget
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where(strings.Join(where, " OR "), args...).
			Order("entity_type, entity_id, COALESCE(locale, '')").Find(&batchRows).Error; err != nil {
			return nil, err
		}
		locked = append(locked, batchRows...)
	}
	return targetsFromRows(locked), nil
}

func (s *store) PointTargets(ctx context.Context, pointers []og.TargetPointer, now time.Time) (int64, error) {
	var updated int64
	for start := 0; start < len(pointers); start += ogBulkWriteBatchSize {
		end := min(start+ogBulkWriteBatchSize, len(pointers))
		var caseSQL strings.Builder
		caseSQL.WriteString("CASE id")
		args := make(structured.Values, 0, (end-start)*2)
		ids := make([]string, 0, end-start)
		for _, pointer := range pointers[start:end] {
			caseSQL.WriteString(" WHEN ? THEN ?")
			args = append(args, pointer.TargetID, pointer.GenerationID)
			ids = append(ids, pointer.TargetID)
		}
		caseSQL.WriteString(" ELSE latest_generation_id END")
		result := s.tx.WithContext(ctx).Model(&model.OgGenerationTarget{}).Where("id IN ?", ids).Updates(structured.Fields{
			"latest_generation_id": gorm.Expr(caseSQL.String(), args...), "updated_at": now,
		})
		if result.Error != nil {
			return updated, result.Error
		}
		updated += result.RowsAffected
	}
	return updated, nil
}

func (s *store) ClaimLatestTarget(ctx context.Context, targetID, generationID string, now time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Model(&model.OgGenerationTarget{}).
		Where("id = ? AND latest_generation_id = ?", targetID, generationID).
		Update("updated_at", now)
	return result.RowsAffected == 1, result.Error
}

func (s *store) ClearTargetPointer(ctx context.Context, targetID string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.OgGenerationTarget{}).Where("id = ?", targetID).
		Updates(structured.Fields{"latest_generation_id": nil, "updated_at": now}).Error
}

func (s *store) LockEntityTargets(ctx context.Context, entityType, entityID string) ([]og.GenerationTarget, error) {
	var rows []model.OgGenerationTarget
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("entity_type = ? AND entity_id = ?", entityType, entityID).
		Order("COALESCE(locale, ''), id").Find(&rows).Error; err != nil {
		return nil, err
	}
	return targetsFromRows(rows), nil
}

func (s *store) Target(ctx context.Context, targetID string) (og.GenerationTarget, error) {
	var row model.OgGenerationTarget
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", targetID).Error; err != nil {
		return og.GenerationTarget{}, notFound(err, og.ErrTargetNotFound)
	}
	return targetFromRow(row), nil
}

func (s *store) FindTarget(ctx context.Context, key og.TargetKey) (og.GenerationTarget, error) {
	query := s.tx.WithContext(ctx).Where("entity_type = ? AND entity_id = ? AND target_kind = ?", key.EntityType, key.EntityID, key.Kind)
	if key.Locale == nil {
		query = query.Where("locale IS NULL")
	} else {
		query = query.Where("locale = ?", *key.Locale)
	}
	var row model.OgGenerationTarget
	if err := query.Take(&row).Error; err != nil {
		return og.GenerationTarget{}, notFound(err, og.ErrTargetNotFound)
	}
	return targetFromRow(row), nil
}

func (s *store) LockGeneration(ctx context.Context, generationID string) (og.Generation, og.GenerationTarget, error) {
	tx := s.tx.WithContext(ctx)
	var generationRef struct {
		TargetID string `gorm:"column:target_id"`
	}
	if err := tx.Model(&model.OgGeneration{}).
		Select("target_id").
		Where("id = ?", generationID).
		Take(&generationRef).Error; err != nil {
		return og.Generation{}, og.GenerationTarget{}, notFound(err, og.ErrGenerationNotFound)
	}
	locking := clause.Locking{Strength: clause.LockingStrengthUpdate}
	var target model.OgGenerationTarget
	if err := tx.Clauses(locking).First(&target, "id = ?", generationRef.TargetID).Error; err != nil {
		return og.Generation{}, og.GenerationTarget{}, notFound(err, og.ErrTargetNotFound)
	}
	var generation model.OgGeneration
	if err := tx.Clauses(locking).First(&generation, "id = ?", generationID).Error; err != nil {
		return og.Generation{}, og.GenerationTarget{}, notFound(err, og.ErrGenerationNotFound)
	}
	return generationFromRow(generation), targetFromRow(target), nil
}

func (s *store) Generation(ctx context.Context, generationID string) (og.Generation, error) {
	var row model.OgGeneration
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", generationID).Error; err != nil {
		return og.Generation{}, notFound(err, og.ErrGenerationNotFound)
	}
	return generationFromRow(row), nil
}

func (s *store) RunGenerations(ctx context.Context, runID string) ([]og.Generation, error) {
	var rows []model.OgGeneration
	if err := s.tx.WithContext(ctx).Where("run_id = ?", runID).Order("request_sequence").Find(&rows).Error; err != nil {
		return nil, err
	}
	generations := make([]og.Generation, len(rows))
	for i, row := range rows {
		generations[i] = generationFromRow(row)
	}
	return generations, nil
}

func (s *store) InsertGenerations(ctx context.Context, generations []og.NewGeneration) error {
	assets := make([]model.PublicAsset, len(generations))
	rows := make([]model.OgGeneration, len(generations))
	for i, generation := range generations {
		assets[i] = model.PublicAsset{
			ID: generation.Output.AssetID, Kind: "og", ObjectKey: generation.Output.ObjectKey,
			Extension: generation.Output.Extension, MimeType: generation.Output.MimeType,
			Disposition: "inline", Status: model.PublicAssetStatusAllocated,
			CreatedAt: generation.CreatedAt, UpdatedAt: generation.UpdatedAt,
		}
		rows[i] = generationRow(generation.Generation)
	}
	tx := s.tx.WithContext(ctx)
	if err := tx.CreateInBatches(&assets, ogBulkWriteBatchSize).Error; err != nil {
		return err
	}
	return tx.CreateInBatches(&rows, ogBulkWriteBatchSize).Error
}

func (s *store) LockActiveGenerations(ctx context.Context, targetID string) ([]og.Generation, error) {
	var rows []model.OgGeneration
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("target_id = ? AND status IN ?", targetID, []string{
			model.OgGenerationStatusQueued,
			model.OgGenerationStatusProcessing,
		}).Order("request_sequence, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	generations := make([]og.Generation, len(rows))
	for i, row := range rows {
		generations[i] = generationFromRow(row)
	}
	return generations, nil
}

func (s *store) SupersedeReplacedGeneration(ctx context.Context, targetID, generationID, replacementID string, now time.Time) (bool, error) {
	updates := structured.Fields{
		"status":           model.OgGenerationStatusSuperseded,
		"superseded_at":    now,
		"superseded_by_id": replacementID,
		"completed_at":     now,
		"updated_at":       now,
	}
	tx := s.tx.WithContext(ctx)
	result := tx.Model(&model.OgGeneration{}).
		Where("id = ? AND target_id = ? AND status IN ?", generationID, targetID, []string{
			model.OgGenerationStatusQueued,
		}).
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		result = tx.Model(&model.OgGeneration{}).
			Where("id = ? AND target_id = ? AND status = ?", generationID, targetID, model.OgGenerationStatusProcessing).
			Updates(updates)
		if result.Error != nil {
			return false, result.Error
		}
	}
	return result.RowsAffected != 0, nil
}

func (s *store) StartGeneration(ctx context.Context, generationID, leaseToken string, leaseExpiresAt, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.OgGeneration{}).Where("id = ?", generationID).Updates(structured.Fields{
		"status":        model.OgGenerationStatusProcessing,
		"processing_at": now, "lease_token": leaseToken, "lease_expires_at": leaseExpiresAt,
		"last_error_code": nil, "updated_at": now,
	}).Error
}

func (s *store) MarkGenerationReady(ctx context.Context, generationID, leaseToken string, now time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Model(&model.OgGeneration{}).
		Where("id = ? AND status = ? AND lease_token = ?", generationID, model.OgGenerationStatusProcessing, leaseToken).
		Updates(structured.Fields{
			"status": model.OgGenerationStatusReady, "ready_at": now,
			"completed_at": now, "updated_at": now,
		})
	return result.RowsAffected == 1, result.Error
}

func (s *store) MarkGenerationFailed(ctx context.Context, generationID string, from og.GenerationStatus, code string, now time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Model(&model.OgGeneration{}).
		Where("id = ? AND status = ?", generationID, string(from)).
		Updates(structured.Fields{
			"status":           model.OgGenerationStatusFailed,
			"failed_at":        now,
			"completed_at":     now,
			"lease_token":      nil,
			"lease_expires_at": nil,
			"last_error_code":  code,
			"updated_at":       now,
		})
	return result.RowsAffected == 1, result.Error
}

func (s *store) MarkGenerationSuperseded(ctx context.Context, generationID string, replacementID *string, clearLease bool, now time.Time) (bool, error) {
	updates := structured.Fields{
		"status":        model.OgGenerationStatusSuperseded,
		"superseded_at": now,
		"completed_at":  now,
		"updated_at":    now,
	}
	if clearLease {
		updates["lease_token"] = nil
		updates["lease_expires_at"] = nil
	}
	if replacementID != nil {
		updates["superseded_by_id"] = *replacementID
	}
	result := s.tx.WithContext(ctx).Model(&model.OgGeneration{}).
		Where("id = ? AND status IN ?", generationID, []string{
			model.OgGenerationStatusQueued,
			model.OgGenerationStatusProcessing,
		}).
		Updates(updates)
	return result.RowsAffected == 1, result.Error
}

func (s *store) MarkGenerationCancelled(ctx context.Context, generationID string, from og.GenerationStatus, now time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Model(&model.OgGeneration{}).
		Where("id = ? AND status = ?", generationID, string(from)).
		Updates(structured.Fields{
			"status": model.OgGenerationStatusCancelled, "cancelled_at": now, "completed_at": now,
			"lease_token": nil, "lease_expires_at": nil,
			"updated_at": now,
		})
	return result.RowsAffected == 1, result.Error
}

func (s *store) InsertRun(ctx context.Context, run og.Run) error {
	row := model.OgGenerationRun{
		ID: run.ID, TriggerKind: run.TriggerKind, Reason: run.Reason,
		RenderConfigSnapshot: run.RenderConfigSnapshot, ConfigRevision: run.ConfigRevision,
		Status: string(run.Status), StartedAt: run.StartedAt, CompletedAt: run.CompletedAt,
		CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
	}
	return s.tx.WithContext(ctx).Create(&row).Error
}

func (s *store) Run(ctx context.Context, runID string) (og.Run, error) {
	var row model.OgGenerationRun
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", runID).Error; err != nil {
		return og.Run{}, notFound(err, og.ErrRunNotFound)
	}
	return og.Run{
		ID: row.ID, TriggerKind: row.TriggerKind, Reason: row.Reason,
		RenderConfigSnapshot: row.RenderConfigSnapshot, ConfigRevision: row.ConfigRevision,
		Status: og.RunStatus(row.Status), StartedAt: row.StartedAt, CompletedAt: row.CompletedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func (s *store) StartRun(ctx context.Context, runID string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.OgGenerationRun{}).
		Where("id = ? AND status = ?", runID, model.OgGenerationRunStatusQueued).
		Updates(structured.Fields{
			"status":     model.OgGenerationRunStatusRunning,
			"started_at": now,
			"updated_at": now,
		}).Error
}

func (s *store) LockRun(ctx context.Context, runID string) error {
	var run model.OgGenerationRun
	err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id").First(&run, "id = ?", runID).Error
	return notFound(err, og.ErrRunNotFound)
}

func (s *store) RunStatusCounts(ctx context.Context, runID string) (map[og.GenerationStatus]int64, error) {
	type countRow struct {
		Status string `gorm:"column:status"`
		Count  int64  `gorm:"column:count"`
	}
	var rows []countRow
	if err := s.tx.WithContext(ctx).Model(&model.OgGeneration{}).
		Select("status, COUNT(*) AS count").
		Where("run_id = ?", runID).
		Group("status").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[og.GenerationStatus]int64, len(rows))
	for _, row := range rows {
		counts[og.GenerationStatus(row.Status)] = row.Count
	}
	return counts, nil
}

func (s *store) SettleRun(ctx context.Context, runID string, status og.RunStatus, completedAt *time.Time, now time.Time) error {
	var completed structured.Value
	if completedAt != nil {
		completed = *completedAt
	}
	return s.tx.WithContext(ctx).Model(&model.OgGenerationRun{}).Where("id = ?", runID).Updates(structured.Fields{
		"status": string(status), "completed_at": completed, "updated_at": now,
		"started_at": gorm.Expr("COALESCE(started_at, created_at, ?)", now),
	}).Error
}

func (s *store) RenderConfigSnapshot(ctx context.Context) ([]byte, string, error) {
	if s.ports.RenderConfig == nil {
		return nil, "", errors.New("OG render config is not bound")
	}
	return s.ports.RenderConfig.Snapshot(ctx, s.tx, s.ports.CDNDomain)
}

func (s *store) ResolvePublicText(ctx context.Context, text string) (string, error) {
	return sitevariable.ResolvePublicText(ctx, s.tx, text)
}

func (s *store) ReadyAssetsForFiles(ctx context.Context, fileIDs []string) (map[string]og.AssetRef, error) {
	var assets []model.PublicAsset
	if err := s.tx.WithContext(ctx).
		Where("source_file_id IN ? AND status = ?", fileIDs, model.PublicAssetStatusReady).
		Order("source_file_id, created_at DESC").Find(&assets).Error; err != nil {
		return nil, err
	}
	lifecycle := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), s.ports.CDNDomain)
	refs := make(map[string]og.AssetRef, len(fileIDs))
	for _, asset := range assets {
		fileID := ""
		if asset.SourceFileID != nil {
			fileID = *asset.SourceFileID
		}
		if _, exists := refs[fileID]; exists {
			continue
		}
		ref, err := lifecycle.AssetRef(mediaassetpostgres.Asset(asset))
		if err != nil {
			return nil, err
		}
		refs[fileID] = refFromProto(ref)
	}
	return refs, nil
}

func (s *store) ResolveRequests(ctx context.Context, entityType, entityID string, selection og.Selection) ([]og.Request, error) {
	if s.ports.Requests == nil {
		return nil, errors.New("OG request sources are not bound")
	}
	return s.ports.Requests.resolve(ctx, s.tx, entityType, entityID, selection)
}

func (s *store) CollectRequests(ctx context.Context) ([]og.Request, error) {
	if s.ports.AllRequests == nil {
		return nil, errors.New("OG all-request sources are not bound")
	}
	return s.ports.AllRequests.collect(ctx, s.tx)
}

func (s *store) ReconcileBeforeGlobalGeneration(ctx context.Context) error {
	if s.ports.Reconciler == nil {
		return errors.New("OG global reconciler is not bound")
	}
	return s.ports.Reconciler.ReconcileBeforeGlobalGeneration(ctx, s.tx, s.ports.CDNDomain)
}

func (s *store) ReleasePendingProjection(ctx context.Context, target og.Target) error {
	projection, err := s.projectionFor(target)
	if err != nil {
		return err
	}
	return projection.ReleasePending(ctx, s.tx, target, s.ports.CDNDomain)
}

func (s *store) CompleteProjection(ctx context.Context, target og.Target, assetID string, now time.Time) error {
	projection, err := s.projectionFor(target)
	if err != nil {
		return err
	}
	return projection.Complete(ctx, s.tx, target, assetID, now, s.ports.CDNDomain)
}

func (s *store) projectionFor(target og.Target) (Projection, error) {
	for _, projection := range s.ports.Projections {
		if projection != nil && projection.Handles(target) {
			return projection, nil
		}
	}
	return nil, domainerr.InvalidEntityType(target.EntityType)
}

func (s *store) CompleteAsset(ctx context.Context, written og.WrittenAsset) (og.Asset, error) {
	asset, err := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), s.ports.CDNDomain).CompletePublicAsset(ctx, mediaasset.WrittenAssetFromProto(&commonv1.AssetWriteResult{
		AssetId: written.AssetID, FileSize: written.FileSize, Sha256: written.SHA256,
	}))
	if err != nil {
		return og.Asset{}, err
	}
	return assetFromModel(mediaassetpostgres.AssetRow(*asset)), nil
}

func (s *store) AssetRef(asset og.Asset) (og.AssetRef, error) {
	ref, err := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), s.ports.CDNDomain).AssetRef(mediaassetpostgres.Asset(assetRow(asset)))
	if err != nil {
		return og.AssetRef{}, err
	}
	return refFromProto(ref), nil
}

func (s *store) Asset(ctx context.Context, assetID string) (og.Asset, bool, error) {
	var asset model.PublicAsset
	if err := s.tx.WithContext(ctx).First(&asset, "id = ?", assetID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return og.Asset{}, false, nil
		}
		return og.Asset{}, false, err
	}
	return assetFromModel(asset), true, nil
}

func (s *store) UnboundReadyAssetIDs(ctx context.Context, cutoff time.Time) ([]string, error) {
	var assetIDs []string
	err := s.tx.WithContext(ctx).Model(&model.PublicAsset{}).
		Where("kind = ? AND status = ? AND ready_at < ?", "og", model.PublicAssetStatusReady, cutoff).
		Where("NOT EXISTS (SELECT 1 FROM public_asset_binding pab WHERE pab.asset_id = public_asset.id)").
		Order("ready_at ASC, id ASC").Pluck("id", &assetIDs).Error
	return assetIDs, err
}

func (s *store) ExpiredTerminalAssetIDs(ctx context.Context, cutoff time.Time) ([]string, error) {
	var assetIDs []string
	err := s.tx.WithContext(ctx).Table("public_asset").Select("public_asset.id").
		Joins("JOIN og_generation ON og_generation.id = public_asset.id").
		Where("public_asset.kind = ?", "og").
		Where("public_asset.status IN ?", []string{model.PublicAssetStatusAllocated, model.PublicAssetStatusFailed}).
		Where("public_asset.created_at < ?", cutoff).
		Where("og_generation.status IN ?", []string{
			model.OgGenerationStatusFailed, model.OgGenerationStatusSuperseded, model.OgGenerationStatusCancelled,
		}).Order("public_asset.created_at ASC, public_asset.id ASC").Pluck("public_asset.id", &assetIDs).Error
	return assetIDs, err
}

func (s *store) LockAsset(ctx context.Context, assetID string) (og.Asset, bool, error) {
	var asset model.PublicAsset
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "kind", "status", "ready_at", "created_at").Where("id = ?", assetID).Take(&asset).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return og.Asset{}, false, nil
		}
		return og.Asset{}, false, err
	}
	return assetFromModel(asset), true, nil
}

func (s *store) AssetBound(ctx context.Context, assetID string) (bool, error) {
	var bindings int64
	if err := s.tx.WithContext(ctx).Model(&model.PublicAssetBinding{}).Where("asset_id = ?", assetID).Count(&bindings).Error; err != nil {
		return false, err
	}
	return bindings != 0, nil
}

func (s *store) AssetReferenced(ctx context.Context, assetID string) (bool, error) {
	for _, reference := range s.ports.References {
		referenced, err := reference.ReferencesAsset(ctx, s.tx, assetID)
		if err != nil || referenced {
			return referenced, err
		}
	}
	return false, nil
}

func (s *store) MarkUnboundAssetDeletePending(ctx context.Context, assetID string, cutoff, now time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Model(&model.PublicAsset{}).
		Where("id = ? AND kind = ? AND status = ? AND ready_at < ?", assetID, "og", model.PublicAssetStatusReady, cutoff).
		Updates(structured.Fields{"status": model.PublicAssetStatusDeletePending, "delete_requested_at": now, "updated_at": now})
	return result.RowsAffected == 1, result.Error
}

func (s *store) MarkTerminalAssetDeletePending(ctx context.Context, assetID string, cutoff, now time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Model(&model.PublicAsset{}).
		Where("id = ? AND kind = ? AND status IN ? AND created_at < ?", assetID, "og", []string{
			model.PublicAssetStatusAllocated, model.PublicAssetStatusFailed,
		}, cutoff).
		Updates(structured.Fields{"status": model.PublicAssetStatusDeletePending, "delete_requested_at": now, "updated_at": now})
	return result.RowsAffected == 1, result.Error
}

func (s *store) EnqueueGeneration(ctx context.Context, generationID string) error {
	if s.tx.Dialector.Name() != "postgres" {
		return nil
	}
	executor, err := contentsignal.TransactionExecutor(s.tx)
	if err != nil {
		return err
	}
	return mq.EnqueueProtobuf(ctx, executor, eventpkg.QueueOgGenerate, generationID, &managev1.OgGenerationJob{GenerationId: generationID})
}

func notFound(err, sentinel error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: %w", sentinel, err)
	}
	return err
}
