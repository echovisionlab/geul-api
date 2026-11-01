package filemedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func dedicatedPublicAssetKind(uploadType managev1.UploadType, slotID string) (string, bool) {
	switch uploadType {
	case managev1.UploadType_UPLOAD_TYPE_USER_AVATAR:
		return "avatar", true
	case managev1.UploadType_UPLOAD_TYPE_ARTIST_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_WORK_FEATURED_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_SERIES_FEATURED_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_FORM_FEATURED_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_SITE_OG_BACKGROUND:
		return "image", true
	case managev1.UploadType_UPLOAD_TYPE_PROGRAM_EVENT_POSTER:
		return "poster", true
	case managev1.UploadType_UPLOAD_TYPE_MAP_IMAGE:
		return "map_image", true
	case managev1.UploadType_UPLOAD_TYPE_RELEASE_ARTWORK:
		return "artwork", true
	case managev1.UploadType_UPLOAD_TYPE_LABEL_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_CLIENT_LOGO:
		return "logo", true
	case managev1.UploadType_UPLOAD_TYPE_SITE_LOGO:
		if isSiteEmailLogoUpload(uploadType, slotID) {
			return "email_image", true
		}
		return "logo", true
	case managev1.UploadType_UPLOAD_TYPE_SITE_FAVICON:
		return "favicon", true
	case managev1.UploadType_UPLOAD_TYPE_SITE_LOADER:
		return "loader", true
	default:
		return "", false
	}
}

func (s *Service) promoteDedicatedPublicUploadAsset(
	ctx context.Context,
	uploadType managev1.UploadType,
	slotID string,
	fileID string,
) (*commonv1.AssetRef, error) {
	if uploadType == managev1.UploadType_UPLOAD_TYPE_SITE_FAVICON {
		return s.generateFaviconBundle(ctx, fileID)
	}
	kind, ok := dedicatedPublicAssetKind(uploadType, slotID)
	if !ok {
		return nil, nil
	}
	return s.promoteSourceFileToPublicAsset(ctx, fileID, kind)
}

func (s *Service) promoteSourceFileToPublicAsset(
	ctx context.Context,
	sourceFileID string,
	kind string,
) (*commonv1.AssetRef, error) {
	sourceFileID = strings.TrimSpace(sourceFileID)
	if !IsValidUUID(sourceFileID) {
		return nil, domainerr.InvalidField("file_id", "invalid UUID format")
	}

	file, asset, target, ready, err := s.prepareSourceAssetPromotion(ctx, sourceFileID, kind)
	if err != nil {
		return nil, err
	}
	lifecycle := mediaasset.NewLifecycle(s.db, s.cdnDomain)
	if ready {
		return lifecycle.ReadyAssetRef(ctx, asset.ID)
	}

	digest, err := s.streamSourceAssetPromotion(ctx, file, target.GetObjectKey())
	if err != nil {
		concurrentReady, failureErr := s.failSourceAssetPromotion(ctx, asset.ID, err)
		if failureErr != nil {
			return nil, fmt.Errorf("%v; record public asset promotion failure: %w", err, failureErr)
		}
		if concurrentReady {
			return lifecycle.ReadyAssetRef(ctx, asset.ID)
		}
		return nil, err
	}
	if err := s.completeSourceAssetPromotion(ctx, file.ID, asset.ID, file.FileSize, digest); err != nil {
		// The verified object is deliberately retained. A completion retry uses
		// the same asset ID and canonical object key, while an ambiguous commit is
		// resolved by the ready-row re-read in completeSourceAssetPromotion.
		return nil, err
	}
	return lifecycle.ReadyAssetRef(ctx, asset.ID)
}

func (s *Service) prepareSourceAssetPromotion(
	ctx context.Context,
	sourceFileID string,
	kind string,
) (model.File, *model.PublicAsset, *commonv1.AssetWriteTarget, bool, error) {
	var lastErr error
	for range 2 {
		var file model.File
		var asset *model.PublicAsset
		var target *commonv1.AssetWriteTarget
		var ready bool
		callbackCompleted := false
		txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := mediaasset.LockAttachableFilesForUpdate(ctx, tx, []string{sourceFileID}); err != nil {
				return err
			}
			if err := tx.
				Select("id", "mime_type", "file_size", "extension").
				Where("id = ?", sourceFileID).
				Take(&file).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return domainerr.ResourceNotFound("file", sourceFileID)
				}
				return err
			}
			if err := validateSourceAssetPromotionFile(file, kind); err != nil {
				return err
			}

			lifecycle := mediaasset.NewLifecycle(tx, s.cdnDomain)
			var err error
			asset, target, ready, err = prepareSourceAssetPromotion(ctx, tx, lifecycle, file, kind)
			if err != nil {
				return err
			}
			callbackCompleted = true
			return nil
		})
		if txErr == nil {
			return file, asset, target, ready, nil
		}
		lastErr = txErr
		if !callbackCompleted {
			return model.File{}, nil, nil, false, txErr
		}

		slog.ErrorContext(ctx, "Source asset allocation commit failed; reconciling", failurecause.Attr(txErr), "file_id", file.ID)
		// A commit acknowledgement can be lost after the allocation/resume row
		// became durable. Re-read the source authority before retrying allocation.
		existing, recoveredTarget, recoveredReady, recoverErr := recoverSourceAssetPromotionAllocation(
			ctx, s.db, file, kind, txErr,
		)
		if recoverErr == nil {
			return file, existing, recoveredTarget, recoveredReady, nil
		}
	}
	return model.File{}, nil, nil, false, lastErr
}

func (s *Service) streamSourceAssetPromotion(
	ctx context.Context,
	file model.File,
	targetObjectKey string,
) ([]byte, error) {
	sourceKey, err := mediaauth.MediaObjectKey(file.ID, file.Extension)
	if err != nil {
		return nil, err
	}
	object, err := s.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.s3Bucket),
		Key:    aws.String(sourceKey),
	})
	if err != nil {
		return nil, fmt.Errorf("read source for public asset: %w", err)
	}
	defer object.Body.Close()
	if aws.ToInt64(object.ContentLength) != file.FileSize ||
		canonicalMimeType(aws.ToString(object.ContentType)) != file.MimeType {
		return nil, domainerr.Precondition("public asset source metadata does not match File")
	}

	hasher := sha256.New()
	body := &countingReader{
		reader: io.TeeReader(io.LimitReader(object.Body, file.FileSize+1), hasher),
	}
	uploader := transfermanager.New(s.s3Client, func(options *transfermanager.Options) {
		options.PartSizeBytes = chunkSize
	})
	if _, err := uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      aws.String(s.s3Bucket),
		Key:         aws.String(targetObjectKey),
		Body:        body,
		ContentType: aws.String(file.MimeType),
	}); err != nil {
		return nil, fmt.Errorf("stream source to public asset: %w", err)
	}
	if body.count != file.FileSize {
		s.deleteS3ObjectBestEffort(ctx, targetObjectKey)
		return nil, domainerr.Precondition("public asset source size changed during promotion")
	}
	return hasher.Sum(nil), nil
}

func validateSourceAssetPromotionFile(file model.File, kind string) error {
	if file.FileSize <= 0 ||
		strings.TrimSpace(file.Extension) == "" || canonicalMimeType(file.MimeType) == "" {
		return domainerr.Precondition("public asset source is missing verified metadata")
	}
	if model.GetExtensionFromMime(file.MimeType) != file.Extension {
		return domainerr.Precondition("public asset source MIME and extension do not match")
	}
	if reason := mediaasset.PublicAssetKindMediaContractViolation(kind, file.Extension, file.MimeType); reason != "" {
		return domainerr.Precondition(reason)
	}
	return nil
}

func prepareSourceAssetPromotion(
	ctx context.Context,
	tx *gorm.DB,
	lifecycle *mediaasset.Lifecycle,
	file model.File,
	kind string,
) (*model.PublicAsset, *commonv1.AssetWriteTarget, bool, error) {
	var existing model.PublicAsset
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("source_file_id = ? AND status <> ?", file.ID, model.PublicAssetStatusDeleted).
		Order("created_at DESC").
		Take(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		asset, target, allocateErr := lifecycle.AllocatePublicAsset(ctx, mediaasset.Allocation{
			SourceFileID: &file.ID,
			Kind:         kind,
			Extension:    file.Extension,
			MimeType:     file.MimeType,
			Disposition:  commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
		})
		return asset, target, false, allocateErr
	}
	if err != nil {
		return nil, nil, false, err
	}
	target, err := sourceAssetPromotionTarget(existing, file, kind)
	if err != nil {
		return nil, nil, false, err
	}

	switch existing.Status {
	case model.PublicAssetStatusReady:
		return &existing, nil, true, nil
	case model.PublicAssetStatusAllocated:
	case model.PublicAssetStatusFailed:
		now := dbtime.Now()
		result := tx.Model(&model.PublicAsset{}).
			Where("id = ? AND status = ?", existing.ID, model.PublicAssetStatusFailed).
			Updates(structured.Fields{
				"file_size":           nil,
				"sha256":              nil,
				"status":              model.PublicAssetStatusAllocated,
				"ready_at":            nil,
				"delete_requested_at": nil,
				"deleted_at":          nil,
				"failed_at":           nil,
				"failure_reason":      nil,
				"updated_at":          now,
			})
		if result.Error != nil {
			return nil, nil, false, result.Error
		}
		if result.RowsAffected != 1 {
			return nil, nil, false, domainerr.Precondition("failed public asset could not be resumed")
		}
		existing.FileSize = nil
		existing.SHA256 = nil
		existing.Status = model.PublicAssetStatusAllocated
		existing.ReadyAt = nil
		existing.DeleteRequestedAt = nil
		existing.DeletedAt = nil
		existing.FailedAt = nil
		existing.FailureReason = nil
		existing.UpdatedAt = now
	default:
		return nil, nil, false, domainerr.Precondition("source file public asset cannot be promoted from its current state")
	}

	return &existing, target, false, nil
}

func loadSourceAssetPromotion(
	ctx context.Context,
	db *gorm.DB,
	file model.File,
	kind string,
) (*model.PublicAsset, *commonv1.AssetWriteTarget, bool, error) {
	var existing model.PublicAsset
	if err := db.WithContext(ctx).
		Where("source_file_id = ? AND status <> ?", file.ID, model.PublicAssetStatusDeleted).
		Order("created_at DESC").
		Take(&existing).Error; err != nil {
		return nil, nil, false, err
	}
	target, err := sourceAssetPromotionTarget(existing, file, kind)
	if err != nil {
		return nil, nil, false, err
	}
	switch existing.Status {
	case model.PublicAssetStatusReady:
		return &existing, target, true, nil
	case model.PublicAssetStatusAllocated, model.PublicAssetStatusFailed:
		return &existing, target, false, nil
	default:
		return nil, nil, false, domainerr.Precondition("source file public asset cannot be promoted from its current state")
	}
}

func recoverSourceAssetPromotionAllocation(
	ctx context.Context,
	db *gorm.DB,
	file model.File,
	kind string,
	commitErr error,
) (*model.PublicAsset, *commonv1.AssetWriteTarget, bool, error) {
	existing, target, ready, err := loadSourceAssetPromotion(ctx, db, file, kind)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to reconcile source asset allocation", failurecause.Attr(err), "file_id", file.ID)
	}
	if err == nil && (ready || existing.Status == model.PublicAssetStatusAllocated) {
		return existing, target, ready, nil
	}
	return nil, nil, false, commitErr
}

func sourceAssetPromotionTarget(
	existing model.PublicAsset,
	file model.File,
	kind string,
) (*commonv1.AssetWriteTarget, error) {
	if existing.Kind != kind || existing.Extension != file.Extension || existing.MimeType != file.MimeType ||
		existing.Disposition != "inline" || existing.DownloadFilename != nil {
		return nil, domainerr.Precondition("source file is already promoted with a different public asset contract")
	}
	expectedObjectKey, err := mediaauth.AssetObjectKey(existing.ID, existing.Extension)
	if err != nil || expectedObjectKey != existing.ObjectKey {
		return nil, domainerr.Precondition("source file public asset has a non-canonical object key").WithCause(failurecause.ServerFault(failurecause.WithAttrs(err, slog.String("asset_id", existing.ID))))
	}
	return &commonv1.AssetWriteTarget{
		AssetId:     existing.ID,
		ObjectKey:   existing.ObjectKey,
		Extension:   existing.Extension,
		MimeType:    existing.MimeType,
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}, nil
}

func (s *Service) failSourceAssetPromotion(ctx context.Context, assetID string, cause error) (bool, error) {
	var lastErr error
	for range 2 {
		now := time.Now().UTC()
		result := s.db.WithContext(ctx).Model(&model.PublicAsset{}).
			Where("id = ? AND status = ?", assetID, model.PublicAssetStatusAllocated).
			Updates(structured.Fields{
				"status":         model.PublicAssetStatusFailed,
				"failed_at":      now,
				"failure_reason": strings.TrimSpace(cause.Error()),
				"updated_at":     now,
			})
		lastErr = result.Error
		if result.Error == nil && result.RowsAffected == 1 {
			return false, nil
		}

		var asset model.PublicAsset
		if err := s.db.WithContext(ctx).Where("id = ?", assetID).Take(&asset).Error; err != nil {
			if lastErr != nil {
				return false, lastErr
			}
			return false, err
		}
		switch asset.Status {
		case model.PublicAssetStatusReady:
			return true, nil
		case model.PublicAssetStatusFailed:
			return false, nil
		case model.PublicAssetStatusAllocated:
			continue
		default:
			return false, domainerr.Precondition("source file public asset cannot fail from its current state")
		}
	}
	if lastErr != nil {
		return false, lastErr
	}
	return false, domainerr.Precondition("public asset promotion allocation changed unexpectedly")
}

// completeLockedSourceAsset completes the source File's promoted public asset,
// resuming it first when an earlier attempt failed.
func (s *Service) completeLockedSourceAsset(
	ctx context.Context,
	tx *gorm.DB,
	sourceFileID string,
	assetID string,
	result *commonv1.AssetWriteResult,
) error {
	// File is the serialization authority for both new product references
	// and deletion. Keep the global File -> public_asset lock order.
	if err := mediaasset.LockAttachableFilesForUpdate(ctx, tx, []string{sourceFileID}); err != nil {
		return err
	}
	var asset model.PublicAsset
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("id = ?", assetID).Take(&asset).Error; err != nil {
		return err
	}
	if asset.SourceFileID == nil || *asset.SourceFileID != sourceFileID {
		return domainerr.Precondition("public asset no longer belongs to the source file")
	}
	if asset.Status == model.PublicAssetStatusFailed {
		now := time.Now().UTC()
		resumed := tx.Model(&model.PublicAsset{}).
			Where("id = ? AND status = ?", assetID, model.PublicAssetStatusFailed).
			Updates(structured.Fields{
				"file_size":      nil,
				"sha256":         nil,
				"status":         model.PublicAssetStatusAllocated,
				"ready_at":       nil,
				"failed_at":      nil,
				"failure_reason": nil,
				"updated_at":     now,
			})
		if resumed.Error != nil {
			return resumed.Error
		}
		if resumed.RowsAffected != 1 {
			return domainerr.Precondition("failed public asset could not be resumed")
		}
	}
	_, err := mediaasset.NewLifecycle(tx, s.cdnDomain).CompletePublicAsset(ctx, result)
	return err
}

func (s *Service) completeSourceAssetPromotion(
	ctx context.Context,
	sourceFileID string,
	assetID string,
	fileSize int64,
	digest []byte,
) error {
	result := &commonv1.AssetWriteResult{AssetId: assetID, FileSize: fileSize, Sha256: append([]byte(nil), digest...)}
	var lastErr error
	for range 2 {
		callbackCompleted := false
		txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := s.completeLockedSourceAsset(ctx, tx, sourceFileID, assetID, result); err != nil {
				return err
			}
			callbackCompleted = true
			return nil
		})
		if txErr == nil {
			return nil
		}
		lastErr = txErr
		if !callbackCompleted {
			return txErr
		}

		slog.ErrorContext(ctx, "Source asset completion commit failed; reconciling", failurecause.Attr(txErr), "file_id", sourceFileID, "asset_id", assetID)
		// Resolve an ambiguous outer commit from the authoritative asset row.
		resolved, recoverErr := recoverSourceAssetPromotionCompletion(
			ctx, s.db, sourceFileID, assetID, fileSize, digest, txErr,
		)
		if recoverErr != nil {
			return recoverErr
		}
		if resolved {
			return nil
		}
	}
	return lastErr
}

func recoverSourceAssetPromotionCompletion(
	ctx context.Context,
	db *gorm.DB,
	sourceFileID string,
	assetID string,
	fileSize int64,
	digest []byte,
	commitErr error,
) (bool, error) {
	var file model.File
	if err := db.WithContext(ctx).
		Select("id", "delete_requested_at").
		Where("id = ?", sourceFileID).
		Take(&file).Error; err != nil || file.DeleteRequestedAt != nil {
		if err != nil {
			slog.ErrorContext(ctx, "Failed to reconcile source file after asset completion", failurecause.Attr(err), "file_id", sourceFileID, "asset_id", assetID)
		}
		return false, commitErr
	}
	var asset model.PublicAsset
	if err := db.WithContext(ctx).Where("id = ?", assetID).Take(&asset).Error; err != nil {
		slog.ErrorContext(ctx, "Failed to reconcile completed source asset", failurecause.Attr(err), "file_id", sourceFileID, "asset_id", assetID)
		return false, commitErr
	}
	if asset.SourceFileID == nil || *asset.SourceFileID != sourceFileID {
		return false, commitErr
	}
	switch asset.Status {
	case model.PublicAssetStatusReady:
		if asset.FileSize != nil && *asset.FileSize == fileSize && bytes.Equal(asset.SHA256, digest) {
			return true, nil
		}
		return false, domainerr.Precondition("public asset completion conflicts with ready metadata")
	case model.PublicAssetStatusAllocated, model.PublicAssetStatusFailed:
		return false, nil
	default:
		return false, commitErr
	}
}
