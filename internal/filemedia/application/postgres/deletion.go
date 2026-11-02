package applicationpostgres

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/application"
	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	transcodepostgres "github.com/echovisionlab/geul-api/internal/transcode/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type deletionUnits struct {
	mutation.UnitOfWork[application.DeletionStore]
	db         *gorm.DB
	references filemediapostgres.AttachmentReferences
}

func NewDeletionUnits(db *gorm.DB, references filemediapostgres.AttachmentReferences) application.DeletionUnits {
	if db == nil || references == nil {
		panic("file deletion units: database and attachment reference owners are required")
	}
	return deletionUnits{UnitOfWork: mutation.Postgres(db, func(tx *gorm.DB) application.DeletionStore { return &store{tx: tx, references: references} }), db: db, references: references}
}
func (u deletionUnits) Compensated(ctx context.Context, work func(application.DeletionStore, application.RegisterCompensation) error) error {
	return executeCompensatedTransaction(ctx, u.db, func(tx *gorm.DB, register registerTransactionCompensation) error {
		if err := authzmutation.LockTransaction(tx); err != nil {
			return err
		}
		return work(&store{tx: tx, references: u.references}, register)
	})
}
func (s *store) LockDeletionFile(ctx context.Context, id string) (filemedia.File, error) {
	var row model.File
	err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id", "extension", "mime_type", "delete_requested_at").Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = storeError{err, filemedia.ErrFileNotFound}
	}
	return file(row), err
}
func (s *store) ActiveFileReferenceNames(ctx context.Context, id string) ([]string, error) {
	return filemediapostgres.ActiveFileReferenceNames(ctx, s.tx, s.references, id)
}
func (s *store) PendingTranscodeResultCounts(ctx context.Context, id string) (int64, int64, error) {
	return transcodepostgres.PendingResultCounts(ctx, s.tx, id)
}
func (s *store) FileGenerations(ctx context.Context, id string) ([]mediaasset.Generation, error) {
	var rows []model.MediaGeneration
	if err := s.tx.WithContext(ctx).Where("file_id = ?", id).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]mediaasset.Generation, len(rows))
	for i, row := range rows {
		result[i] = mediaasset.Generation{ID: row.ID, FileID: row.FileID, Kind: row.Kind, ObjectPrefix: row.ObjectPrefix, ManifestName: row.ManifestName, ManifestSHA256: row.ManifestSHA256, ObjectCount: row.ObjectCount, TotalSize: row.TotalSize, Status: mediaasset.GenerationStatus(row.Status), ReadyAt: row.ReadyAt, RetiredAt: row.RetiredAt, DeleteAfter: row.DeleteAfter, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	}
	return result, nil
}
func (s *store) PendingMeshResultCount(ctx context.Context, id string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).Table("mesh_optimization_candidate").Where("source_file_id = ? AND selected_at IS NULL", id).Where("status IN ?", []string{model.MeshOptimizationCandidateStatusPending, model.MeshOptimizationCandidateStatusProcessing, model.MeshOptimizationCandidateStatusCancelled}).Count(&count).Error
	return count, err
}
func (s *store) DeleteRequestedFile(ctx context.Context, id string) (bool, error) {
	result := s.tx.WithContext(ctx).Where("id = ? AND delete_requested_at IS NOT NULL", id).Delete(&model.File{})
	return result.RowsAffected != 0, result.Error
}
func (s *store) FileExists(ctx context.Context, id string) (bool, error) {
	var count int64
	err := s.tx.WithContext(ctx).Table("file").Where("id = ?", id).Count(&count).Error
	return count != 0, err
}

type storeError struct {
	error
	kind error
}

func (e storeError) Is(target error) bool { return target == e.kind }
func (e storeError) Unwrap() error        { return e.error }
