package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/model"
	transcodestate "github.com/echovisionlab/geul-api/internal/transcode"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	db                *gorm.DB
	fileAuthorization AuthorizationDeletion
}

func NewDeletion(db *gorm.DB, authorization AuthorizationDeletion) *Deletion {
	if db == nil {
		panic("filemedia deletion: db is required")
	}
	return &Deletion{db: db, fileAuthorization: authorization}
}

func (h *Deletion) ValidatePending(
	ctx context.Context,
	event *managev1.FileDeleteEvent,
) (bool, error) {
	var exists bool
	err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var file model.File
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "extension", "mime_type", "delete_requested_at").
			Where("id = ?", event.GetFileId()).
			First(&file).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return fmt.Errorf("load pending file deletion: %w", err)
		}
		if err := validatePendingFileDeletionWithDB(ctx, tx, event, file); err != nil {
			return err
		}
		exists = true
		return nil
	})
	return exists, err
}

func validatePendingFileDeletionWithDB(
	ctx context.Context,
	db *gorm.DB,
	event *managev1.FileDeleteEvent,
	file model.File,
) error {
	if file.DeleteRequestedAt == nil {
		return fmt.Errorf("file %s has no deletion request", event.GetFileId())
	}
	expected, err := filemedia.CanonicalMediaObjectTargetForFile(file)
	if err != nil {
		return fmt.Errorf("resolve pending file deletion target: %w", err)
	}
	original := event.GetOriginal()
	if original.GetFileId() != expected.GetFileId() ||
		original.GetObjectKey() != expected.GetObjectKey() ||
		original.GetExtension() != expected.GetExtension() ||
		original.GetMimeType() != expected.GetMimeType() {
		return fmt.Errorf("file %s deletion target does not match domain state", event.GetFileId())
	}
	references, err := filemedia.ActiveFileReferenceNames(ctx, db, event.GetFileId())
	if err != nil {
		return fmt.Errorf("recheck file references before storage deletion: %w", err)
	}
	if len(references) > 0 {
		return fmt.Errorf("%w: %s", filemedia.ErrFileStillReferenced, strings.Join(references, ", "))
	}
	if len(event.GetAssets()) != 0 {
		return fmt.Errorf("%w: full file deletion must not own public asset deletion", ErrInvalidFileDeleteTarget)
	}
	var pendingTranscodeResults int64
	if err := db.WithContext(ctx).
		Table("transcode_job").
		Where("file_id = ? AND status IN ?", event.GetFileId(), []string{
			transcodestate.StatusQueued,
			transcodestate.StatusProcessing,
			transcodestate.StatusCancelled,
		}).
		Count(&pendingTranscodeResults).Error; err != nil {
		return fmt.Errorf("check pending transcode results before file deletion: %w", err)
	}
	var pendingWaveformResults int64
	if err := db.WithContext(ctx).
		Table("waveform_job").
		Where("file_id = ? AND status IN ?", event.GetFileId(), []string{
			transcodestate.WaveformJobStatusQueued,
			transcodestate.WaveformJobStatusCancelled,
		}).
		Count(&pendingWaveformResults).Error; err != nil {
		return fmt.Errorf("check pending waveform results before file deletion: %w", err)
	}
	if pendingTranscodeResults > 0 || pendingWaveformResults > 0 {
		return fmt.Errorf(
			"file %s still has %d pending transcode and %d pending waveform result(s)",
			event.GetFileId(),
			pendingTranscodeResults,
			pendingWaveformResults,
		)
	}
	var generations []model.MediaGeneration
	if err := db.WithContext(ctx).
		Where("file_id = ?", event.GetFileId()).
		Order("id ASC").
		Find(&generations).Error; err != nil {
		return fmt.Errorf("load file media generations before deletion: %w", err)
	}
	if err := validateExactFileMediaGenerationTargets(event, generations); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFileDeleteTarget, err)
	}
	return nil
}

func validateExactFileMediaGenerationTargets(
	event *managev1.FileDeleteEvent,
	rows []model.MediaGeneration,
) error {
	if len(event.GetGenerations()) != len(rows) {
		return fmt.Errorf("generation target set does not match domain state")
	}
	byID := make(map[string]model.MediaGeneration, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	seen := make(map[string]struct{}, len(rows))
	for _, target := range event.GetGenerations() {
		if target == nil {
			return fmt.Errorf("generation target is required")
		}
		if _, duplicate := seen[target.GetGenerationId()]; duplicate {
			return fmt.Errorf("generation target is duplicated")
		}
		seen[target.GetGenerationId()] = struct{}{}
		row, ok := byID[target.GetGenerationId()]
		if !ok || row.FileID != event.GetFileId() || row.Kind != "hls" ||
			row.ObjectPrefix != target.GetObjectPrefix() || target.GetFileId() != row.FileID {
			return fmt.Errorf("generation target does not match domain state")
		}
	}
	return nil
}

func (h *Deletion) Finalize(ctx context.Context, event *managev1.FileDeleteEvent) error {
	return executeCompensatedTransaction(ctx, h.db, func(tx *gorm.DB, registerCompensation registerTransactionCompensation) error {
		if err := authzmutation.LockTransaction(tx); err != nil {
			return err
		}
		var file model.File
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "extension", "mime_type", "delete_requested_at").
			Where("id = ?", event.GetFileId()).
			First(&file).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return fmt.Errorf("reload pending file deletion: %w", err)
		}
		if err := validatePendingFileDeletionWithDB(ctx, tx, event, file); err != nil {
			return err
		}
		var pendingMeshResults int64
		if err := tx.
			Table("mesh_optimization_candidate").
			Where("source_file_id = ? AND selected_at IS NULL", event.GetFileId()).
			Where("status IN ?", []string{
				model.MeshOptimizationCandidateStatusPending,
				model.MeshOptimizationCandidateStatusProcessing,
				model.MeshOptimizationCandidateStatusCancelled,
			}).
			Count(&pendingMeshResults).Error; err != nil {
			return fmt.Errorf("check pending mesh optimization results before file finalization: %w", err)
		}
		if pendingMeshResults > 0 {
			return fmt.Errorf("file %s still has %d pending mesh optimization result(s)", event.GetFileId(), pendingMeshResults)
		}
		resource, err := policyv1.File.Resource(event.GetFileId())
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

		result := tx.
			Where("id = ? AND delete_requested_at IS NOT NULL", event.GetFileId()).
			Delete(&model.File{})
		if result.Error != nil {
			return fmt.Errorf("finalize file deletion: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			var count int64
			if err := tx.Table("file").Where("id = ?", event.GetFileId()).Count(&count).Error; err != nil {
				return fmt.Errorf("verify file deletion finalization: %w", err)
			}
			if count != 0 {
				return fmt.Errorf("file %s deletion request changed before finalization", event.GetFileId())
			}
		}
		return nil
	})
}
