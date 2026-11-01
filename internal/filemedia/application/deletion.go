package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type AuthorizationDeletion interface {
	DeleteAndVerify(context.Context, policyv1.Resource) (
		restore func(context.Context) error,
		spiceDBConfirmedAt time.Time,
		err error,
	)
}

var ErrInvalidFileDeleteTarget = errors.New("invalid file delete target")

type Deletion struct {
	units             DeletionUnits
	fileAuthorization AuthorizationDeletion
}

func NewDeletion(units DeletionUnits, authorization AuthorizationDeletion) *Deletion {
	if units == nil {
		panic("filemedia deletion: units are required")
	}
	return &Deletion{units: units, fileAuthorization: authorization}
}

func (h *Deletion) ValidatePending(
	ctx context.Context,
	event *filemedia.DeletionTarget,
) (bool, error) {
	var exists bool
	err := h.units.Transaction(ctx, false, func(unit mutation.Tx[DeletionStore]) error {
		store := unit.Store()
		file, err := store.LockDeletionFile(ctx, event.FileID)
		if errors.Is(err, filemedia.ErrFileNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load pending file deletion: %w", err)
		}
		if err := validatePendingFileDeletion(ctx, store, event, file); err != nil {
			return err
		}
		exists = true
		return nil
	})
	return exists, err
}

func validatePendingFileDeletion(
	ctx context.Context,
	store DeletionStore,
	event *filemedia.DeletionTarget,
	file filemedia.File,
) error {
	if file.DeleteRequestedAt == nil {
		return fmt.Errorf("file %s has no deletion request", event.FileID)
	}
	expected, err := filemedia.CanonicalObjectTarget(file)
	if err != nil {
		return fmt.Errorf("resolve pending file deletion target: %w", err)
	}
	original := event.Original
	if original == nil || original.FileID != expected.FileID ||
		original.ObjectKey != expected.ObjectKey ||
		original.Extension != expected.Extension ||
		original.MimeType != expected.MimeType {
		return fmt.Errorf("file %s deletion target does not match domain state", event.FileID)
	}
	references, err := store.ActiveFileReferenceNames(ctx, event.FileID)
	if err != nil {
		return fmt.Errorf("recheck file references before storage deletion: %w", err)
	}
	if len(references) > 0 {
		return fmt.Errorf("%w: %s", filemedia.ErrFileStillReferenced, strings.Join(references, ", "))
	}
	if event.HasPublicAssets {
		return fmt.Errorf("%w: full file deletion must not own public asset deletion", ErrInvalidFileDeleteTarget)
	}
	pendingTranscodeResults, pendingWaveformResults, err := store.PendingTranscodeResultCounts(ctx, event.FileID)
	if err != nil {
		return fmt.Errorf("check pending transcode results before file deletion: %w", err)
	}
	if pendingTranscodeResults > 0 || pendingWaveformResults > 0 {
		return fmt.Errorf(
			"file %s still has %d pending transcode and %d pending waveform result(s)",
			event.FileID,
			pendingTranscodeResults,
			pendingWaveformResults,
		)
	}
	generations, err := store.FileGenerations(ctx, event.FileID)
	if err != nil {
		return fmt.Errorf("load file media generations before deletion: %w", err)
	}
	if err := validateExactFileMediaGenerationTargets(event, generations); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFileDeleteTarget, err)
	}
	return nil
}

func validateExactFileMediaGenerationTargets(
	event *filemedia.DeletionTarget,
	rows []mediaasset.Generation,
) error {
	if len(event.Generations) != len(rows) {
		return fmt.Errorf("generation target set does not match domain state")
	}
	byID := make(map[string]mediaasset.Generation, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	seen := make(map[string]struct{}, len(rows))
	for _, target := range event.Generations {
		if target == nil {
			return fmt.Errorf("generation target is required")
		}
		if _, duplicate := seen[target.GenerationID]; duplicate {
			return fmt.Errorf("generation target is duplicated")
		}
		seen[target.GenerationID] = struct{}{}
		row, ok := byID[target.GenerationID]
		if !ok || row.FileID != event.FileID || row.Kind != "hls" ||
			row.ObjectPrefix != target.ObjectPrefix || target.FileID != row.FileID {
			return fmt.Errorf("generation target does not match domain state")
		}
	}
	return nil
}

func (h *Deletion) Finalize(ctx context.Context, event *filemedia.DeletionTarget) error {
	return h.units.Compensated(ctx, func(store DeletionStore, registerCompensation RegisterCompensation) error {
		file, err := store.LockDeletionFile(ctx, event.FileID)
		if errors.Is(err, filemedia.ErrFileNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reload pending file deletion: %w", err)
		}
		if err := validatePendingFileDeletion(ctx, store, event, file); err != nil {
			return err
		}
		if err := requireNoPendingMeshResults(ctx, store, event.FileID); err != nil {
			return err
		}
		resource, err := policyv1.File.Resource(event.FileID)
		if err != nil {
			return fmt.Errorf("build File authorization resource: %w", err)
		}
		if h.fileAuthorization == nil {
			return fmt.Errorf("file authorization deletion is required")
		}
		restoreAuthorization, spiceDBConfirmedAt, err := h.fileAuthorization.DeleteAndVerify(ctx, resource)
		if err != nil {
			return err
		}
		if err := registerCompensation(spiceDBConfirmedAt, restoreAuthorization); err != nil {
			return err
		}

		return deleteRequestedFile(ctx, store, event.FileID)
	})
}

func requireNoPendingMeshResults(ctx context.Context, store DeletionStore, fileID string) error {
	count, err := store.PendingMeshResultCount(ctx, fileID)
	if err != nil {
		return fmt.Errorf("check pending mesh optimization results before file finalization: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("file %s still has %d pending mesh optimization result(s)", fileID, count)
	}
	return nil
}

// deleteRequestedFile deletes the File row while its deletion request stands;
// a row that is already gone counts as deleted.
func deleteRequestedFile(ctx context.Context, store DeletionStore, fileID string) error {
	deleted, err := store.DeleteRequestedFile(ctx, fileID)
	if err != nil {
		return fmt.Errorf("finalize file deletion: %w", err)
	}
	if !deleted {
		exists, err := store.FileExists(ctx, fileID)
		if err != nil {
			return fmt.Errorf("verify file deletion finalization: %w", err)
		}
		if exists {
			return fmt.Errorf("file %s deletion request changed before finalization", fileID)
		}
	}
	return nil
}
