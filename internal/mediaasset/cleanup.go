package mediaasset

import (
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/transcode"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	OrphanHLSPrefixRetention          = 24 * time.Hour
	RetiredGenerationCleanupBatchSize = 100
)

type StoredObject struct {
	Key          string
	LastModified time.Time
}

type CleanupObjectStore interface {
	ListObjects(context.Context) ([]StoredObject, error)
	DeleteObject(context.Context, string) error
	DeletePrefix(context.Context, string) error
}

type Cleanup struct {
	db      *gorm.DB
	objects CleanupObjectStore
}

func NewCleanup(db *gorm.DB, objects CleanupObjectStore) *Cleanup {
	if db == nil {
		panic("media asset cleanup: database is required")
	}
	if objects == nil {
		panic("media asset cleanup: object store is required")
	}
	return &Cleanup{db: db, objects: objects}
}

func (s *Cleanup) CleanupDangling(ctx context.Context, now time.Time) error {
	retiredErr := s.CleanupRetiredGenerations(ctx, now.UTC())
	hlsErr := s.CleanupOrphanHLSPrefixes(ctx, now.UTC().Add(-OrphanHLSPrefixRetention))
	return stderrors.Join(retiredErr, hlsErr)
}

type retiredGenerationCleanupTarget struct {
	ID           string
	FileID       string
	ObjectPrefix string
	DeleteAfter  time.Time
}

func (s *Cleanup) CleanupRetiredGenerations(ctx context.Context, now time.Time) error {
	var generationIDs []string
	if err := s.db.WithContext(ctx).
		Model(&model.MediaGeneration{}).
		Where("status = ? AND delete_after IS NOT NULL AND delete_after <= ?", model.MediaGenerationStatusRetired, now).
		Order("delete_after ASC, id ASC").
		Limit(RetiredGenerationCleanupBatchSize).
		Pluck("id", &generationIDs).Error; err != nil {
		return fmt.Errorf("list due retired media generations: %w", err)
	}

	deleted := 0
	var cleanupErrs []error
	for _, generationID := range generationIDs {
		removed, err := s.cleanupRetiredGeneration(ctx, generationID, now)
		if err != nil {
			cleanupErrs = append(cleanupErrs, err)
			continue
		}
		if removed {
			deleted++
		}
	}
	if deleted > 0 {
		slog.Info("Cleaned retired media generations", "count", deleted)
	}
	return stderrors.Join(cleanupErrs...)
}

func (s *Cleanup) cleanupRetiredGeneration(ctx context.Context, generationID string, now time.Time) (bool, error) {
	target, err := s.prepareRetiredGenerationCleanup(ctx, generationID, now)
	if err != nil || target == nil {
		return false, err
	}
	if err := s.objects.DeletePrefix(ctx, target.ObjectPrefix+"/"); err != nil {
		return false, fmt.Errorf("delete retired media generation prefix %s: %w", target.ID, err)
	}
	return s.finalizeRetiredGenerationCleanup(ctx, *target, now)
}

func (s *Cleanup) prepareRetiredGenerationCleanup(ctx context.Context, generationID string, now time.Time) (*retiredGenerationCleanupTarget, error) {
	var target *retiredGenerationCleanupTarget
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		generation, eligible, err := lockDueUnreferencedRetiredGeneration(tx, generationID, now)
		if err != nil || !eligible {
			return err
		}
		target = &retiredGenerationCleanupTarget{
			ID: generation.ID, FileID: generation.FileID, ObjectPrefix: generation.ObjectPrefix,
			DeleteAfter: generation.DeleteAfter.UTC(),
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("prepare retired media generation %s cleanup: %w", generationID, err)
	}
	return target, nil
}

func (s *Cleanup) finalizeRetiredGenerationCleanup(ctx context.Context, target retiredGenerationCleanupTarget, now time.Time) (bool, error) {
	removed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		generation, eligible, err := lockDueUnreferencedRetiredGeneration(tx, target.ID, now)
		if err != nil {
			return err
		}
		if generation.ID == "" {
			return nil
		}
		if !eligible {
			return fmt.Errorf("media generation changed after prefix deletion")
		}
		if generation.FileID != target.FileID || generation.ObjectPrefix != target.ObjectPrefix ||
			generation.DeleteAfter == nil || !generation.DeleteAfter.UTC().Equal(target.DeleteAfter) {
			return fmt.Errorf("media generation identity changed after prefix deletion")
		}
		result := tx.Where("id = ?", target.ID).Delete(&model.MediaGeneration{})
		if result.Error != nil {
			return fmt.Errorf("delete retired media generation row %s: %w", target.ID, result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("media generation row %s changed before deletion", target.ID)
		}
		removed = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("finalize retired media generation %s cleanup: %w", target.ID, err)
	}
	return removed, nil
}

func lockDueUnreferencedRetiredGeneration(tx *gorm.DB, generationID string, now time.Time) (model.MediaGeneration, bool, error) {
	var generation model.MediaGeneration
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "file_id", "status", "object_prefix", "delete_after").
		Where("id = ?", generationID).Take(&generation).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return model.MediaGeneration{}, false, nil
		}
		return model.MediaGeneration{}, false, err
	}
	if generation.Status != model.MediaGenerationStatusRetired || generation.DeleteAfter == nil || generation.DeleteAfter.After(now) {
		return generation, false, nil
	}
	var pendingFileDeletion int64
	if err := tx.Model(&model.File{}).
		Where("id = ? AND delete_requested_at IS NOT NULL", generation.FileID).
		Count(&pendingFileDeletion).Error; err != nil {
		return model.MediaGeneration{}, false, fmt.Errorf("check media generation %s file deletion intent: %w", generation.ID, err)
	}
	if pendingFileDeletion != 0 {
		return generation, false, nil
	}
	expectedPrefix, err := mediaauth.MediaHLSObjectPrefix(generation.FileID, generation.ID)
	if err != nil || generation.ObjectPrefix != expectedPrefix {
		return model.MediaGeneration{}, false, fmt.Errorf("media generation %s has invalid object prefix", generation.ID)
	}
	var references int64
	if err := tx.Model(&model.FileDerivative{}).Where("media_generation_id = ?", generation.ID).Count(&references).Error; err != nil {
		return model.MediaGeneration{}, false, fmt.Errorf("check media generation %s current derivative: %w", generation.ID, err)
	}
	return generation, references == 0, nil
}

type HLSPrefixInventory struct {
	Prefix         string
	FileID         string
	GenerationID   string
	LatestModified time.Time
	HasManifest    bool
}

func (s *Cleanup) CleanupOrphanHLSPrefixes(ctx context.Context, cutoff time.Time) error {
	objects, err := s.objects.ListObjects(ctx)
	if err != nil {
		return fmt.Errorf("list HLS prefix inventory: %w", err)
	}
	inventory := BuildHLSPrefixInventory(objects)
	if len(inventory) == 0 {
		return nil
	}

	generationIDs := make([]string, 0, len(inventory))
	fileIDs := make([]string, 0, len(inventory))
	for _, prefix := range inventory {
		if !prefix.HasManifest && prefix.FileID != "" && prefix.GenerationID != "" {
			generationIDs = append(generationIDs, prefix.GenerationID)
			fileIDs = append(fileIDs, prefix.FileID)
		}
	}
	if len(fileIDs) == 0 {
		return nil
	}
	referenced, err := s.loadReferencedHLSGenerationIDs(ctx, generationIDs)
	if err != nil {
		return fmt.Errorf("load referenced HLS generations: %w", err)
	}
	active, err := s.loadActiveTranscodeFileIDs(ctx, fileIDs)
	if err != nil {
		return fmt.Errorf("load active transcode jobs for HLS cleanup: %w", err)
	}
	deletable, inconsistent := SelectOrphanHLSPrefixesForCleanup(inventory, referenced, active, cutoff)
	for _, prefix := range inconsistent {
		slog.Warn("Skipping HLS prefix cleanup because DB still references missing manifest",
			"prefix", prefix.Prefix, "fileId", prefix.FileID, "generationId", prefix.GenerationID)
	}
	deleted := 0
	for _, prefix := range deletable {
		if err := s.objects.DeletePrefix(ctx, prefix.Prefix); err != nil {
			slog.Warn("Failed to delete orphan HLS prefix", "prefix", prefix.Prefix, "fileId", prefix.FileID, "error", err)
			continue
		}
		deleted++
	}
	if deleted > 0 {
		slog.Info("Cleaned orphan HLS prefixes", "count", deleted)
	}
	return nil
}

func BuildHLSPrefixInventory(objects []StoredObject) []HLSPrefixInventory {
	byPrefix := make(map[string]*HLSPrefixInventory)
	for _, object := range objects {
		prefix, fileID, generationID, ok := ParseHLSPrefixFromObjectKey(object.Key)
		if !ok {
			continue
		}
		entry := byPrefix[prefix]
		if entry == nil {
			entry = &HLSPrefixInventory{Prefix: prefix, FileID: fileID, GenerationID: generationID}
			byPrefix[prefix] = entry
		}
		if object.LastModified.After(entry.LatestModified) {
			entry.LatestModified = object.LastModified
		}
		if strings.HasSuffix(object.Key, "/master.m3u8") {
			entry.HasManifest = true
		}
	}
	result := make([]HLSPrefixInventory, 0, len(byPrefix))
	for _, entry := range byPrefix {
		result = append(result, *entry)
	}
	return result
}

func ParseHLSPrefixFromObjectKey(key string) (prefix, fileID, generationID string, ok bool) {
	parts := strings.Split(strings.TrimSpace(key), "/")
	if len(parts) < 5 {
		return "", "", "", false
	}
	canonical, err := mediaauth.MediaHLSObjectPrefix(parts[1], parts[3])
	if err != nil || strings.Join(parts[:4], "/") != canonical {
		return "", "", "", false
	}
	return canonical, parts[1], parts[3], true
}

func SelectOrphanHLSPrefixesForCleanup(
	inventory []HLSPrefixInventory,
	referencedGenerationIDs map[string]struct{},
	activeFileIDs map[string]struct{},
	cutoff time.Time,
) (deletable, inconsistent []HLSPrefixInventory) {
	for _, prefix := range inventory {
		if prefix.HasManifest || prefix.FileID == "" || prefix.GenerationID == "" || prefix.LatestModified.After(cutoff) {
			continue
		}
		if _, ok := referencedGenerationIDs[prefix.GenerationID]; ok {
			inconsistent = append(inconsistent, prefix)
			continue
		}
		if _, ok := activeFileIDs[prefix.FileID]; ok {
			continue
		}
		deletable = append(deletable, prefix)
	}
	return deletable, inconsistent
}

func (s *Cleanup) loadReferencedHLSGenerationIDs(ctx context.Context, generationIDs []string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	if len(generationIDs) == 0 {
		return result, nil
	}
	var ids []string
	if err := s.db.WithContext(ctx).Model(&model.FileDerivative{}).
		Where("type = ? AND media_generation_id IN ?", managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(), generationIDs).
		Pluck("media_generation_id", &ids).Error; err != nil {
		return nil, err
	}
	for _, id := range ids {
		result[id] = struct{}{}
	}
	return result, nil
}

func (s *Cleanup) loadActiveTranscodeFileIDs(ctx context.Context, fileIDs []string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	if len(fileIDs) == 0 {
		return result, nil
	}
	var ids []string
	if err := s.db.WithContext(ctx).Model(&model.TranscodeJob{}).
		Where("file_id IN ? AND status IN ?", fileIDs, []string{transcode.StatusQueued, transcode.StatusProcessing}).
		Pluck("file_id", &ids).Error; err != nil {
		return nil, err
	}
	for _, id := range ids {
		result[id] = struct{}{}
	}
	return result, nil
}
