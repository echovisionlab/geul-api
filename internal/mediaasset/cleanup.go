package mediaasset

import (
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/mutation"
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
	units   mutation.UnitOfWork[CleanupStore]
	jobs    ActiveTranscodes
	objects CleanupObjectStore
}

func NewCleanup(units mutation.UnitOfWork[CleanupStore], objects CleanupObjectStore, jobs ActiveTranscodes) *Cleanup {
	if units == nil {
		panic("media asset cleanup: units are required")
	}
	if objects == nil {
		panic("media asset cleanup: object store is required")
	}
	if jobs == nil {
		panic("media asset cleanup: active transcodes are required")
	}
	return &Cleanup{units: units, objects: objects, jobs: jobs}
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
	if err := s.units.Read(ctx, func(store CleanupStore) error {
		var err error
		generationIDs, err = store.DueRetiredGenerationIDs(ctx, now)
		return err
	}); err != nil {
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
		slog.InfoContext(ctx, "Cleaned retired media generations", "count", deleted)
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
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[CleanupStore]) error {
		tx := unit.Store()
		generation, eligible, err := lockDueUnreferencedRetiredGeneration(ctx, tx, generationID, now)
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
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[CleanupStore]) error {
		tx := unit.Store()
		generation, eligible, err := lockDueUnreferencedRetiredGeneration(ctx, tx, target.ID, now)
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
		deleted, err := tx.DeleteGeneration(ctx, target.ID)
		if err != nil {
			return fmt.Errorf("delete retired media generation row %s: %w", target.ID, err)
		}
		if !deleted {
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

func lockDueUnreferencedRetiredGeneration(ctx context.Context, store CleanupStore, generationID string, now time.Time) (Generation, bool, error) {
	generation, err := store.LockRetiredGeneration(ctx, generationID)
	if stderrors.Is(err, ErrGenerationNotFound) {
		return Generation{}, false, nil
	}
	if err != nil {
		return Generation{}, false, err
	}
	if generation.Status != GenerationRetired || generation.DeleteAfter == nil || generation.DeleteAfter.After(now) {
		return generation, false, nil
	}
	pendingFileDeletion, err := store.FileDeletionPending(ctx, generation.FileID)
	if err != nil {
		return Generation{}, false, fmt.Errorf("check media generation %s file deletion intent: %w", generation.ID, err)
	}
	if pendingFileDeletion {
		return generation, false, nil
	}
	expectedPrefix, err := mediaauth.MediaHLSObjectPrefix(generation.FileID, generation.ID)
	if err != nil {
		return Generation{}, false, failurecause.WithAttrs(
			fmt.Errorf("media generation %s has invalid object prefix: %w", generation.ID, err),
			slog.String("file_id", generation.FileID))
	}
	if generation.ObjectPrefix != expectedPrefix {
		return Generation{}, false, fmt.Errorf("media generation %s has invalid object prefix", generation.ID)
	}
	referenced, err := store.GenerationReferenced(ctx, generation.ID)
	if err != nil {
		return Generation{}, false, fmt.Errorf("check media generation %s current derivative: %w", generation.ID, err)
	}
	return generation, !referenced, nil
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
	active, err := s.jobs.ActiveFileIDs(ctx, fileIDs)
	if err != nil {
		return fmt.Errorf("load active transcode jobs for HLS cleanup: %w", err)
	}
	deletable, inconsistent := SelectOrphanHLSPrefixesForCleanup(inventory, referenced, active, cutoff)
	for _, prefix := range inconsistent {
		slog.WarnContext(ctx, "Skipping HLS prefix cleanup because DB still references missing manifest",
			"prefix", prefix.Prefix, "file_id", prefix.FileID, "generation_id", prefix.GenerationID)
	}
	deleted := 0
	for _, prefix := range deletable {
		if err := s.objects.DeletePrefix(ctx, prefix.Prefix); err != nil {
			slog.ErrorContext(ctx, "Failed to delete orphan HLS prefix; retrying on the next cleanup", "prefix", prefix.Prefix, "file_id", prefix.FileID, failurecause.Attr(err))
			continue
		}
		deleted++
	}
	if deleted > 0 {
		slog.InfoContext(ctx, "Cleaned orphan HLS prefixes", "count", deleted)
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

func (s *Cleanup) loadReferencedHLSGenerationIDs(ctx context.Context, ids []string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	if len(ids) == 0 {
		return result, nil
	}
	err := s.units.Read(ctx, func(store CleanupStore) error {
		var err error
		result, err = store.ReferencedGenerationIDs(ctx, ids)
		return err
	})
	return result, err
}
