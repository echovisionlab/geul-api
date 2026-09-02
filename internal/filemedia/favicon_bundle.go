package filemedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	stderrors "errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

const (
	faviconBundleTimeout      = 20 * time.Second
	faviconCleanupItemTimeout = 3 * time.Second
)

var errFaviconBundleCommitUncertain = stderrors.New("favicon bundle commit outcome is uncertain")

func (s *FileService) generateFaviconBundle(
	ctx context.Context,
	sourceFileID string,
) (*commonv1.AssetRef, error) {
	bundleCtx, cancel := context.WithTimeout(ctx, s.faviconGenerationTimeout())
	defer cancel()
	sourceFileID = strings.TrimSpace(sourceFileID)

	existing, err := favicon.LoadSet(bundleCtx, s.db, s.cdnDomain, sourceFileID)
	if err != nil {
		return nil, errs.FailedPrecondition(fmt.Sprintf("existing favicon bundle is invalid: %v", err))
	}
	if existing != nil {
		return existing.GetIconPng_32(), nil
	}

	source, sourceBytes, err := s.loadValidatedFaviconSource(bundleCtx, sourceFileID)
	if err != nil {
		return nil, err
	}
	outputs, err := s.generateFaviconOutputs(bundleCtx, sourceBytes, source.MimeType)
	if err != nil {
		return nil, err
	}
	staged, err := s.stageFaviconBundle(ctx, bundleCtx, sourceBytes, source.MimeType, outputs)
	if err != nil {
		return nil, err
	}
	return s.commitFaviconBundle(ctx, bundleCtx, source, staged)
}

func (s *FileService) faviconGenerationTimeout() time.Duration {
	if s.testFaviconBundleTimeout > 0 {
		return s.testFaviconBundleTimeout
	}
	return faviconBundleTimeout
}

func (s *FileService) loadValidatedFaviconSource(
	ctx context.Context,
	sourceFileID string,
) (model.File, []byte, error) {
	var source model.File
	if err := s.db.WithContext(ctx).
		Select("id", "mime_type", "file_size", "extension").
		Where("id = ?", sourceFileID).
		Take(&source).Error; err != nil {
		return model.File{}, nil, errs.Internal(fmt.Errorf("load favicon source: %w", err))
	}
	objectKey, err := mediaauth.MediaObjectKey(source.ID, source.Extension)
	if err != nil {
		return model.File{}, nil, errs.FailedPrecondition("favicon source metadata is not canonical")
	}
	object, err := s.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.s3Bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return model.File{}, nil, errs.Internal(fmt.Errorf("read favicon source: %w", err))
	}
	sourceBytes, readErr := io.ReadAll(io.LimitReader(object.Body, favicon.MaxSourceSize+1))
	closeErr := object.Body.Close()
	if readErr != nil {
		return model.File{}, nil, errs.Internal(fmt.Errorf("read favicon source: %w", readErr))
	}
	if closeErr != nil {
		return model.File{}, nil, errs.Internal(fmt.Errorf("close favicon source: %w", closeErr))
	}
	if int64(len(sourceBytes)) != source.FileSize {
		return model.File{}, nil, errs.FailedPrecondition("favicon source size does not match File")
	}
	if err := favicon.ValidateSource(sourceBytes, source.MimeType); err != nil {
		return model.File{}, nil, errs.FailedPrecondition(fmt.Sprintf("favicon source validation failed: %v", err))
	}
	return source, sourceBytes, nil
}

func (s *FileService) generateFaviconOutputs(
	ctx context.Context,
	sourceBytes []byte,
	sourceMime string,
) ([]favicon.Output, error) {
	processor := s.faviconProcessor
	if processor == nil {
		processor = favicon.NewProcessor()
	}
	// Process an isolated copy so the exact bytes validated above remain the
	// only bytes eligible for source-backed SVG publication.
	outputs, err := processor.Process(ctx, append([]byte(nil), sourceBytes...), sourceMime)
	if err != nil {
		return nil, errs.FailedPrecondition(fmt.Sprintf("favicon processing failed: %v", err))
	}
	if err := favicon.ValidateOutputs(outputs, sourceMime); err != nil {
		return nil, errs.FailedPrecondition(fmt.Sprintf("favicon processing produced an invalid bundle: %v", err))
	}
	return outputs, nil
}

func (s *FileService) stageFaviconBundle(
	cleanupCtx context.Context,
	ctx context.Context,
	sourceBytes []byte,
	sourceMime string,
	outputs []favicon.Output,
) ([]stagedFaviconAsset, error) {
	staged := make([]stagedFaviconAsset, 0, len(outputs)+1)
	if canonicalMimeType(sourceMime) == "image/svg+xml" {
		asset, stageErr := s.stageFaviconAsset(ctx, sourceBytes, mediaasset.Allocation{
			Kind:        "favicon",
			Extension:   "svg",
			MimeType:    "image/svg+xml",
			Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
		}, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_UNSPECIFIED, true)
		if asset.AssetID != "" {
			staged = append(staged, asset)
		}
		if stageErr != nil {
			s.scheduleStagedFaviconDeletion(cleanupCtx, staged, nil)
			return nil, errs.Internal(fmt.Errorf("stage favicon SVG source asset: %w", stageErr))
		}
	}
	for _, output := range outputs {
		asset, stageErr := s.stageFaviconAsset(ctx, output.Data, mediaasset.Allocation{
			Kind:        "favicon",
			Extension:   output.Spec.Extension,
			MimeType:    output.Spec.MimeType,
			Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
		}, output.Spec.DerivativeType, false)
		if asset.AssetID != "" {
			staged = append(staged, asset)
		}
		if stageErr != nil {
			s.scheduleStagedFaviconDeletion(cleanupCtx, staged, nil)
			return nil, errs.Internal(fmt.Errorf(
				"stage favicon derivative %s: %w",
				output.Spec.DerivativeType.String(),
				stageErr,
			))
		}
	}
	return staged, nil
}

func (s *FileService) commitFaviconBundle(
	cleanupCtx context.Context,
	ctx context.Context,
	source model.File,
	staged []stagedFaviconAsset,
) (*commonv1.AssetRef, error) {
	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		s.scheduleStagedFaviconDeletion(cleanupCtx, staged, nil)
		return nil, errs.Internal(fmt.Errorf("begin favicon bundle transaction: %w", tx.Error))
	}
	set, usedStaged, callbackErr := s.finalizeStagedFaviconBundle(ctx, tx, source, staged)
	if callbackErr != nil {
		rollbackErr := tx.Rollback().Error
		s.scheduleStagedFaviconDeletion(cleanupCtx, staged, nil)
		return nil, errs.Internal(fmt.Errorf(
			"store generated favicon bundle: %w",
			stderrors.Join(callbackErr, rollbackErr),
		))
	}
	commitErr := tx.Commit().Error
	if commitErr == nil && s.testFaviconCommitError != nil {
		commitErr = s.testFaviconCommitError
	}
	if commitErr != nil {
		winner, reconcileErr := s.reconcileFaviconBundle(cleanupCtx, source.ID)
		if reconcileErr == nil && winner != nil {
			s.scheduleStagedFaviconDeletion(cleanupCtx, staged, favicon.AssetIDs(winner))
			return winner.GetIconPng_32(), nil
		}
		slog.Error("favicon bundle commit outcome could not be reconciled; preserving source and staged assets",
			"file_id", source.ID, "commit_error", commitErr, "reconcile_error", reconcileErr)
		return nil, errs.Internal(fmt.Errorf(
			"%w: commit error: %v; reconcile error: %v",
			errFaviconBundleCommitUncertain,
			commitErr,
			reconcileErr,
		))
	}
	if !usedStaged {
		s.scheduleStagedFaviconDeletion(cleanupCtx, staged, favicon.AssetIDs(set))
	}
	return set.GetIconPng_32(), nil
}

type stagedFaviconAsset struct {
	AssetID        string
	DerivativeType managev1.FileDerivativeType
	SourceSVG      bool
}

func (s *FileService) stageFaviconAsset(
	ctx context.Context,
	data []byte,
	allocation mediaasset.Allocation,
	derivativeType managev1.FileDerivativeType,
	sourceSVG bool,
) (stagedFaviconAsset, error) {
	lifecycle := mediaasset.NewLifecycle(s.db, s.cdnDomain)
	_, target, err := lifecycle.AllocatePublicAsset(ctx, allocation)
	if err != nil {
		return stagedFaviconAsset{}, err
	}
	staged := stagedFaviconAsset{
		AssetID:        target.GetAssetId(),
		DerivativeType: derivativeType,
		SourceSVG:      sourceSVG,
	}
	if _, err := s.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.s3Bucket),
		Key:           aws.String(target.GetObjectKey()),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
		ContentType:   aws.String(allocation.MimeType),
	}); err != nil {
		return staged, err
	}
	digest := sha256.Sum256(data)
	if _, err := lifecycle.CompletePublicAsset(ctx, &commonv1.AssetWriteResult{
		AssetId:  target.GetAssetId(),
		FileSize: int64(len(data)),
		Sha256:   digest[:],
	}); err != nil {
		return staged, err
	}
	return staged, nil
}

func (s *FileService) finalizeStagedFaviconBundle(
	ctx context.Context,
	tx *gorm.DB,
	source model.File,
	staged []stagedFaviconAsset,
) (*commonv1.FaviconAssetSet, bool, error) {
	var lockedSource model.File
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "mime_type", "file_size", "extension").
		Where("id = ?", source.ID).
		Take(&lockedSource).Error; err != nil {
		return nil, false, err
	}
	existing, err := favicon.LoadSet(ctx, tx, s.cdnDomain, source.ID)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return existing, false, nil
	}

	stagedByType := make(map[managev1.FileDerivativeType]stagedFaviconAsset, len(staged))
	for _, asset := range staged {
		if asset.SourceSVG {
			result := tx.WithContext(ctx).Model(&model.PublicAsset{}).
				Where(
					"id = ? AND kind = ? AND status = ? AND source_file_id IS NULL",
					asset.AssetID,
					"favicon",
					model.PublicAssetStatusReady,
				).
				Updates(structured.Fields{
					"source_file_id": source.ID,
					"updated_at":     time.Now().UTC(),
				})
			if result.Error != nil {
				return nil, false, result.Error
			}
			if result.RowsAffected != 1 {
				return nil, false, fmt.Errorf("staged SVG favicon asset is not attachable")
			}
			continue
		}
		stagedByType[asset.DerivativeType] = asset
	}
	for _, spec := range favicon.RequiredOutputs() {
		asset, ok := stagedByType[spec.DerivativeType]
		if !ok {
			return nil, false, fmt.Errorf("staged favicon bundle is missing %s", spec.DerivativeType.String())
		}
		if err := tx.WithContext(ctx).Table("file_derivative").Create(structured.Fields{
			"id":       uuid.NewString(),
			"file_id":  source.ID,
			"type":     spec.DerivativeType.String(),
			"asset_id": asset.AssetID,
		}).Error; err != nil {
			return nil, false, err
		}
	}
	set, err := favicon.LoadSet(ctx, tx, s.cdnDomain, source.ID)
	if err != nil {
		return nil, false, err
	}
	if set == nil || set.GetIconPng_32() == nil {
		return nil, false, fmt.Errorf("generated favicon set is incomplete")
	}
	return set, true, nil
}

func (s *FileService) reconcileFaviconBundle(
	ctx context.Context,
	sourceFileID string,
) (*commonv1.FaviconAssetSet, error) {
	if s.testFaviconReconcileError != nil {
		return nil, s.testFaviconReconcileError
	}
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), faviconCleanupItemTimeout)
	defer cancel()
	return favicon.LoadSet(reconcileCtx, s.db, s.cdnDomain, sourceFileID)
}

func (s *FileService) scheduleStagedFaviconDeletion(
	ctx context.Context,
	staged []stagedFaviconAsset,
	preserve map[string]struct{},
) {
	ids := make([]string, 0, len(staged))
	for _, asset := range staged {
		if _, keep := preserve[asset.AssetID]; !keep && asset.AssetID != "" {
			ids = append(ids, asset.AssetID)
		}
	}
	if len(ids) == 0 {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), faviconCleanupItemTimeout)
	defer cancel()
	now := time.Now().UTC()
	result := s.db.WithContext(cleanupCtx).Model(&model.PublicAsset{}).
		Where("id IN ? AND status IN ?", ids, []string{model.PublicAssetStatusReady, model.PublicAssetStatusFailed}).
		Updates(structured.Fields{
			"status":              model.PublicAssetStatusDeletePending,
			"delete_requested_at": now,
			"updated_at":          now,
		})
	if result.Error != nil {
		slog.Warn("failed to schedule staged favicon assets for durable cleanup", "asset_ids", ids, "error", result.Error)
	}
}
