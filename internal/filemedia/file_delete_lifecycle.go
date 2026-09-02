package filemedia

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

type FileDeletePublisher interface {
	PublishFileDelete(context.Context, *managev1.FileDeleteEvent) error
}

type transactionalFileDeletePublisher interface {
	PublishFileDeleteWithExecutor(context.Context, eventpkg.DBTX, *managev1.FileDeleteEvent) error
}

type fileDeleteAsyncPublisher struct {
	publisher AsyncPublisher
}

func (p fileDeleteAsyncPublisher) PublishFileDelete(
	ctx context.Context,
	event *managev1.FileDeleteEvent,
) error {
	return publishDurableProto(ctx, p.publisher, eventpkg.QueueFileDelete, event.GetFileId(), event)
}

func (p fileDeleteAsyncPublisher) PublishFileDeleteWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	event *managev1.FileDeleteEvent,
) error {
	transactionalPublisher, ok := p.publisher.(TransactionalAsyncPublisher)
	if !ok {
		return fmt.Errorf("file delete publisher must support transactional enqueue")
	}
	return transactionalPublisher.EnqueueProtobufWithExecutor(
		ctx,
		executor,
		eventpkg.QueueFileDelete,
		event.GetFileId(),
		event,
	)
}

var ErrFileStillReferenced = errors.New("file still has active product references")

type FileDeletionMutation struct {
	FileID               string
	RelatedOutputFileIDs []string
	AlreadyPending       bool
}

// DeleteFile durably requests file deletion. The worker removes the database
// record only after its object deletion succeeds. Direct requests are admin-only.
func (s *FileService) DeleteFile(
	ctx context.Context,
	req *connect.Request[managev1.DeleteFileRequest],
) (*connect.Response[managev1.DeleteFileResponse], error) {
	fileID := strings.TrimSpace(req.Msg.FileId)
	can, err := policyv1.File.Delete(fileID)
	if err != nil {
		return nil, errs.InvalidArgument("file_id", "must be a canonical resource UUID")
	}
	var file struct {
		ID string `gorm:"column:id"`
	}
	if err := s.db.WithContext(ctx).Table("file").Where("id = ?", fileID).First(&file).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("file", fileID)
		}
		return nil, errs.Internal(err)
	}

	if err := s.deleteFileRecordByIDWithCan(ctx, fileID, &can); err != nil {
		return nil, errs.Wrap(err)
	}

	return connect.NewResponse(&managev1.DeleteFileResponse{
		Success: true,
	}), nil
}

type fileMediaGenerationDeleteRef struct {
	ID           string `gorm:"column:id"`
	FileID       string `gorm:"column:file_id"`
	Kind         string `gorm:"column:kind"`
	ObjectPrefix string `gorm:"column:object_prefix"`
}

func getFileDeletionGenerationTargets(
	ctx context.Context,
	db *gorm.DB,
	fileID string,
) ([]*commonv1.MediaGenerationWriteTarget, error) {
	var generations []fileMediaGenerationDeleteRef
	if err := db.WithContext(ctx).
		Table("media_generation").
		Select("id", "file_id", "kind", "object_prefix").
		Where("file_id = ?", fileID).
		Order("id ASC").
		Find(&generations).Error; err != nil {
		return nil, err
	}
	return classifyFileMediaGenerationDeleteRefs(fileID, generations)
}

func classifyFileMediaGenerationDeleteRefs(
	fileID string,
	generationRows []fileMediaGenerationDeleteRef,
) ([]*commonv1.MediaGenerationWriteTarget, error) {
	generations := make([]*commonv1.MediaGenerationWriteTarget, 0, len(generationRows))
	for _, generation := range generationRows {
		expectedPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generation.ID)
		if err != nil || generation.FileID != fileID || generation.Kind != "hls" ||
			generation.ObjectPrefix != expectedPrefix {
			return nil, fmt.Errorf("media generation does not match file deletion authority")
		}
		generations = append(generations, &commonv1.MediaGenerationWriteTarget{
			GenerationId: generation.ID,
			FileId:       fileID,
			ObjectPrefix: generation.ObjectPrefix,
		})
	}
	return generations, nil
}

// requestUnboundFileDerivativeAssetsDeletion moves ready, unbound derivative
// assets into their own durable deletion lifecycle before the source file row
// can cascade its file_derivative relations. Bound assets remain independent
// delivery facts and must not be removed with the original file.
func requestUnboundFileDerivativeAssetsDeletion(
	ctx context.Context,
	tx *gorm.DB,
	fileID string,
) error {
	var assetIDs []string
	if err := tx.WithContext(ctx).
		Table("file_derivative").
		Distinct("asset_id").
		Where("file_id = ? AND asset_id IS NOT NULL", fileID).
		Where("type NOT LIKE ?", "FILE_DERIVATIVE_TYPE_FAVICON_%").
		Order("asset_id ASC").
		Pluck("asset_id", &assetIDs).Error; err != nil {
		return err
	}
	if len(assetIDs) == 0 {
		return nil
	}

	var assets []model.PublicAsset
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", assetIDs).
		Order("id ASC").
		Find(&assets).Error; err != nil {
		return err
	}
	if len(assets) != len(assetIDs) {
		return errs.FailedPrecondition("file derivative references a missing public asset")
	}

	var boundAssetIDs []string
	if err := tx.WithContext(ctx).
		Model(&model.PublicAssetBinding{}).
		Distinct("asset_id").
		Where("asset_id IN ?", assetIDs).
		Pluck("asset_id", &boundAssetIDs).Error; err != nil {
		return err
	}
	bound := make(map[string]struct{}, len(boundAssetIDs))
	for _, assetID := range boundAssetIDs {
		bound[assetID] = struct{}{}
	}

	lifecycle := mediaasset.NewLifecycle(tx, "")
	for _, asset := range assets {
		if _, retained := bound[asset.ID]; retained {
			continue
		}
		switch asset.Status {
		case model.PublicAssetStatusReady, model.PublicAssetStatusFailed:
			if err := lifecycle.RequestPublicAssetDeletion(ctx, asset.ID); err != nil {
				return err
			}
		case model.PublicAssetStatusDeletePending, model.PublicAssetStatusDeleted,
			model.PublicAssetStatusAllocated:
			// Pending/deleted rows already have a cleanup owner. An allocated
			// result cannot become ready after the source file deletion intent;
			// the existing unready-asset retention path reclaims it.
			continue
		default:
			return errs.FailedPrecondition("file derivative public asset has an unsupported lifecycle state")
		}
	}
	return nil
}

// requestUnboundSourceFileAssetsDeletion transfers File-scoped public
// projections to the public-asset deletion lifecycle only when the File itself
// is being deleted. Releasing a domain role binding must not retire a reusable
// File projection while the verified File remains in the library.
func requestUnboundSourceFileAssetsDeletion(
	ctx context.Context,
	tx *gorm.DB,
	fileID string,
) error {
	var assets []model.PublicAsset
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("source_file_id = ?", fileID).
		Order("id ASC").
		Find(&assets).Error; err != nil {
		return err
	}
	if len(assets) == 0 {
		return nil
	}

	assetIDs := make([]string, 0, len(assets))
	for _, asset := range assets {
		assetIDs = append(assetIDs, asset.ID)
	}
	var boundAssetIDs []string
	if err := tx.WithContext(ctx).
		Model(&model.PublicAssetBinding{}).
		Distinct("asset_id").
		Where("asset_id IN ?", assetIDs).
		Pluck("asset_id", &boundAssetIDs).Error; err != nil {
		return err
	}
	bound := make(map[string]struct{}, len(boundAssetIDs))
	for _, assetID := range boundAssetIDs {
		bound[assetID] = struct{}{}
	}

	lifecycle := mediaasset.NewLifecycle(tx, "")
	for _, asset := range assets {
		if _, retained := bound[asset.ID]; retained {
			continue
		}
		switch asset.Status {
		case model.PublicAssetStatusReady, model.PublicAssetStatusFailed:
			if err := lifecycle.RequestPublicAssetDeletion(ctx, asset.ID); err != nil {
				return err
			}
		case model.PublicAssetStatusDeletePending, model.PublicAssetStatusDeleted,
			model.PublicAssetStatusAllocated:
			continue
		default:
			return errs.FailedPrecondition("source File public asset has an unsupported lifecycle state")
		}
	}
	return nil
}

// DeleteFileByID durably records the deletion request and its file.delete
// command in one database transaction. The worker removes the file row only
// after every S3 target has been deleted.
func (s *FileService) DeleteFileByID(ctx context.Context, fileID string) error {
	return s.deleteFileRecordByID(ctx, fileID)
}

func (s *FileService) deleteFileRecordByID(ctx context.Context, fileID string) error {
	return s.deleteFileRecordByIDWithCan(ctx, fileID, nil)
}

func (s *FileService) deleteFileRecordByIDWithCan(ctx context.Context, fileID string, can *policyv1.Can) error {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return nil
	}
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if can != nil {
			if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, *can); err != nil {
				return err
			}
		}
		mutation, err := markFileDeletionRequestedWithDB(ctx, tx, fileID, now)
		if err != nil || mutation == nil {
			return err
		}
		if !mutation.AlreadyPending {
			if err := appendFileDeletedAudit(ctx, tx, s.auditWriter, fileID); err != nil {
				return err
			}
		}
		return publishFileDeletionMutationWithDB(ctx, tx, fileDeleteAsyncPublisher{s.asyncPublisher}, mutation, now)
	})
}

// RequestFileDeletion records the product intent and its stable PGMQ command in
// the same PostgreSQL transaction.
func RequestFileDeletion(
	ctx context.Context,
	db *gorm.DB,
	publisher FileDeletePublisher,
	fileID string,
) error {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return nil
	}
	if db == nil {
		return fmt.Errorf("database is required")
	}
	if publisher == nil {
		return fmt.Errorf("file delete publisher is required")
	}

	now := time.Now().UTC()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		mutation, err := MarkFileDeletionRequestedWithDB(ctx, tx, fileID, now)
		if err != nil || mutation == nil {
			return err
		}
		return publishFileDeletionMutationWithDB(ctx, tx, publisher, mutation, now)
	})
}

// MarkFileDeletionRequestedWithDB records only the product deletion intent.
// The caller owns the transaction and must enqueue the stable command before
// committing it.
func MarkFileDeletionRequestedWithDB(
	ctx context.Context,
	tx *gorm.DB,
	fileID string,
	now time.Time,
) (*FileDeletionMutation, error) {
	return markFileDeletionRequestedWithDB(ctx, tx, fileID, now)
}

func markFileDeletionRequestedWithDB(
	ctx context.Context,
	tx *gorm.DB,
	fileID string,
	now time.Time,
) (*FileDeletionMutation, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return nil, nil
	}
	var file model.File
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "delete_requested_at").
		Where("id = ?", fileID).
		First(&file).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	if file.DeleteRequestedAt != nil {
		return &FileDeletionMutation{FileID: fileID, AlreadyPending: true}, nil
	}
	references, err := ActiveFileReferenceNames(ctx, tx, fileID)
	if err != nil {
		return nil, err
	}
	if len(references) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrFileStillReferenced, strings.Join(references, ", "))
	}
	return markLockedFileDeletionRequestedWithDB(ctx, tx, file, now)
}

func markLockedFileDeletionRequestedWithDB(
	ctx context.Context,
	tx *gorm.DB,
	file model.File,
	now time.Time,
) (*FileDeletionMutation, error) {
	fileID := file.ID
	if err := favicon.RequestDeletion(ctx, tx, fileID); err != nil {
		return nil, err
	}
	if err := requestUnboundFileDerivativeAssetsDeletion(ctx, tx, fileID); err != nil {
		return nil, err
	}
	if err := requestUnboundSourceFileAssetsDeletion(ctx, tx, fileID); err != nil {
		return nil, err
	}
	relatedOutputFileIDs, err := prepareMeshCandidatesForSourceFileDeletion(ctx, tx, fileID, now)
	if err != nil {
		return nil, err
	}
	result := tx.WithContext(ctx).Table("file").Where("id = ? AND delete_requested_at IS NULL", fileID).Update("delete_requested_at", now)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, fmt.Errorf("file %s deletion intent changed while locked", fileID)
	}
	return &FileDeletionMutation{
		FileID:               fileID,
		RelatedOutputFileIDs: relatedOutputFileIDs,
	}, nil
}

func publishFileDeletionMutationWithDB(
	ctx context.Context,
	tx *gorm.DB,
	publisher FileDeletePublisher,
	mutation *FileDeletionMutation,
	now time.Time,
) error {
	for _, outputFileID := range mutation.RelatedOutputFileIDs {
		if outputFileID == mutation.FileID {
			continue
		}
		outputMutation, err := MarkFileDeletionRequestedWithDB(ctx, tx, outputFileID, now)
		if err != nil {
			return err
		}
		if outputMutation != nil {
			if err := publishFileDeletionMutationWithDB(ctx, tx, publisher, outputMutation, now); err != nil {
				return err
			}
		}
	}
	event, err := buildFileDeleteEventWithDB(ctx, tx, mutation.FileID)
	if err != nil {
		return err
	}
	return publishFileDeleteWithDB(ctx, tx, publisher, event)
}

func publishFileDeleteWithDB(
	ctx context.Context,
	tx *gorm.DB,
	publisher FileDeletePublisher,
	event *managev1.FileDeleteEvent,
) error {
	transactionalPublisher, ok := publisher.(transactionalFileDeletePublisher)
	if !ok {
		return fmt.Errorf("file delete publisher must support transactional enqueue")
	}
	executor, ok := tx.Statement.ConnPool.(eventpkg.DBTX)
	if !ok {
		return fmt.Errorf("database transaction does not expose a PGMQ executor")
	}
	return transactionalPublisher.PublishFileDeleteWithExecutor(ctx, executor, event)
}

func prepareMeshCandidatesForSourceFileDeletion(
	ctx context.Context,
	tx *gorm.DB,
	sourceFileID string,
	now time.Time,
) ([]string, error) {
	var candidates []model.MeshOptimizationCandidate
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("source_file_id = ? AND selected_at IS NULL", sourceFileID).
		Find(&candidates).Error; err != nil {
		return nil, err
	}

	outputFileIDs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		switch candidate.Status {
		case model.MeshOptimizationCandidateStatusPending,
			model.MeshOptimizationCandidateStatusProcessing,
			model.MeshOptimizationCandidateStatusCancelled:
			// Keep the allocation until a terminal event retires its output. The
			// file.delete consumer refuses to finalize the source while this row
			// exists, so a late writer can never become an untracked S3 object.
			if err := tx.WithContext(ctx).Model(&model.MeshOptimizationCandidate{}).
				Where("id = ?", candidate.ID).
				Updates(structured.Fields{
					"status":       model.MeshOptimizationCandidateStatusCancelled,
					"cancelled_at": now,
					"expires_at":   now.Add(meshOptimizationCandidateTTL),
					"updated_at":   now,
				}).Error; err != nil {
				return nil, err
			}
		default:
			if candidate.OutputFileID != nil && strings.TrimSpace(*candidate.OutputFileID) != "" {
				var count int64
				if err := tx.WithContext(ctx).Table("file").
					Where("id = ?", strings.TrimSpace(*candidate.OutputFileID)).
					Count(&count).Error; err != nil {
					return nil, err
				}
				if count > 0 {
					outputFileIDs = append(outputFileIDs, strings.TrimSpace(*candidate.OutputFileID))
				}
			}
			if err := tx.WithContext(ctx).
				Where("id = ?", candidate.ID).
				Delete(&model.MeshOptimizationCandidate{}).Error; err != nil {
				return nil, err
			}
		}
	}
	return outputFileIDs, nil
}

func buildFileDeleteEventWithDB(
	ctx context.Context,
	db *gorm.DB,
	fileID string,
) (*managev1.FileDeleteEvent, error) {
	var file model.File
	if err := db.WithContext(ctx).
		Select("id", "extension", "mime_type", "delete_requested_at").
		Where("id = ? AND delete_requested_at IS NOT NULL", fileID).
		First(&file).Error; err != nil {
		return nil, err
	}

	original, err := CanonicalMediaObjectTargetForFile(file)
	if err != nil {
		return nil, err
	}
	generations, err := getFileDeletionGenerationTargets(ctx, db, fileID)
	if err != nil {
		return nil, err
	}
	return &managev1.FileDeleteEvent{
		Original:    original,
		FileId:      fileID,
		Generations: generations,
		Timestamp:   timestamppb.Now(),
	}, nil
}

func (s *FileService) DeleteEditorFileByID(
	ctx context.Context,
	fileID string,
	entityType managev1.TranscodeEntityType,
	entityID string,
	reason managev1.TranscodeCancelReason,
) error {
	if strings.TrimSpace(fileID) == "" {
		return nil
	}
	s.publishTranscodeCancelIfSupported(ctx, fileID, entityType, entityID, reason)
	return s.DeleteFileByID(ctx, fileID)
}
