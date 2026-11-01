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

	"github.com/echovisionlab/geul-api/internal/mediamime"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/mutation"
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
) (*mediaasset.Reference, error) {
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
) (*mediaasset.Reference, error) {
	sourceFileID = strings.TrimSpace(sourceFileID)
	if !IsValidUUID(sourceFileID) {
		return nil, domainerr.InvalidField("file_id", "invalid UUID format")
	}

	file, asset, target, ready, err := s.prepareSourceAssetPromotion(ctx, sourceFileID, kind)
	if err != nil {
		return nil, err
	}
	if ready {
		return s.readySourcePromotionReference(ctx, asset.ID)
	}

	digest, err := s.streamSourceAssetPromotion(ctx, file, target.ObjectKey)
	if err != nil {
		concurrentReady, failureErr := s.failSourceAssetPromotion(ctx, asset.ID, err)
		if failureErr != nil {
			return nil, fmt.Errorf("%v; record public asset promotion failure: %w", err, failureErr)
		}
		if concurrentReady {
			return s.readySourcePromotionReference(ctx, asset.ID)
		}
		return nil, err
	}
	if err := s.completeSourceAssetPromotion(ctx, file.ID, asset.ID, file.FileSize, digest); err != nil {
		// The verified object is deliberately retained. A completion retry uses
		// the same asset ID and canonical object key, while an ambiguous commit is
		// resolved by the ready-row re-read in completeSourceAssetPromotion.
		return nil, err
	}
	return s.readySourcePromotionReference(ctx, asset.ID)
}

func (s *Service) prepareSourceAssetPromotion(
	ctx context.Context,
	sourceFileID string,
	kind string,
) (File, *mediaasset.Asset, *mediaasset.WriteTarget, bool, error) {
	var lastErr error
	for range 2 {
		var file File
		var asset *mediaasset.Asset
		var target *mediaasset.WriteTarget
		var ready bool
		callbackCompleted := false
		var err error
		txErr := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
			store := unit.Store()
			file, err = store.PromotionFile(ctx, sourceFileID, true)
			if err != nil {
				if errors.Is(err, domain.ErrFileNotFound) {
					return domainerr.ResourceNotFound("file", sourceFileID)
				}
				return err
			}
			if err := validateSourceAssetPromotionFile(file, kind); err != nil {
				return err
			}

			asset, target, ready, err = prepareSourceAssetPromotion(ctx, store, s.cdnDomain, file, kind)
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
			return File{}, nil, nil, false, txErr
		}

		slog.ErrorContext(ctx, "Source asset allocation commit failed; reconciling", failurecause.Attr(txErr), "file_id", file.ID)
		// A commit acknowledgement can be lost after the allocation/resume row
		// became durable. Re-read the source authority before retrying allocation.
		existing, recoveredTarget, recoveredReady, recoverErr := recoverSourceAssetPromotionAllocation(
			ctx, s.units, file, kind, txErr,
		)
		if recoverErr == nil {
			return file, existing, recoveredTarget, recoveredReady, nil
		}
	}
	return File{}, nil, nil, false, lastErr
}

func (s *Service) streamSourceAssetPromotion(
	ctx context.Context,
	file File,
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

func validateSourceAssetPromotionFile(file File, kind string) error {
	if file.FileSize <= 0 ||
		strings.TrimSpace(file.Extension) == "" || canonicalMimeType(file.MimeType) == "" {
		return domainerr.Precondition("public asset source is missing verified metadata")
	}
	if mediamime.GetExtensionFromMime(file.MimeType) != file.Extension {
		return domainerr.Precondition("public asset source MIME and extension do not match")
	}
	if reason := mediaasset.PublicAssetKindMediaContractViolation(kind, file.Extension, file.MimeType); reason != "" {
		return domainerr.Precondition(reason)
	}
	return nil
}

func prepareSourceAssetPromotion(
	ctx context.Context,
	store Store,
	cdnDomain string,
	file File,
	kind string,
) (*mediaasset.Asset, *mediaasset.WriteTarget, bool, error) {
	existing, err := store.SourcePromotionAsset(ctx, file.ID, true)
	if errors.Is(err, mediaasset.ErrAssetNotFound) {
		asset, target, allocateErr := store.AllocatePromotionAsset(ctx, cdnDomain, mediaasset.Allocation{SourceFileID: &file.ID, Kind: kind, Extension: file.Extension, MimeType: file.MimeType, Disposition: mediaasset.DispositionInline})
		if allocateErr != nil {
			return nil, nil, false, allocateErr
		}
		return asset, target, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	target, err := sourceAssetPromotionTarget(existing, file, kind)
	if err != nil {
		return nil, nil, false, err
	}

	switch existing.Status {
	case mediaasset.AssetReady:
		return &existing, nil, true, nil
	case mediaasset.AssetAllocated:
	case mediaasset.AssetFailed:
		now := dbtime.Now()
		resumed, resumeErr := store.ResumePromotionAsset(ctx, existing.ID, true, now)
		if resumeErr != nil {
			return nil, nil, false, resumeErr
		}
		if !resumed {
			return nil, nil, false, domainerr.Precondition("failed public asset could not be resumed")
		}
		existing.FileSize = nil
		existing.SHA256 = nil
		existing.Status = mediaasset.AssetAllocated
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
	reader fileStoreReader,
	file File,
	kind string,
) (*mediaasset.Asset, *mediaasset.WriteTarget, bool, error) {
	var existing mediaasset.Asset
	if err := reader.Read(ctx, func(store Store) error {
		var err error
		existing, err = store.SourcePromotionAsset(ctx, file.ID, false)
		return err
	}); err != nil {
		return nil, nil, false, err
	}
	target, err := sourceAssetPromotionTarget(existing, file, kind)
	if err != nil {
		return nil, nil, false, err
	}
	switch existing.Status {
	case mediaasset.AssetReady:
		return &existing, target, true, nil
	case mediaasset.AssetAllocated, mediaasset.AssetFailed:
		return &existing, target, false, nil
	default:
		return nil, nil, false, domainerr.Precondition("source file public asset cannot be promoted from its current state")
	}
}

func recoverSourceAssetPromotionAllocation(
	ctx context.Context,
	reader fileStoreReader,
	file File,
	kind string,
	commitErr error,
) (*mediaasset.Asset, *mediaasset.WriteTarget, bool, error) {
	existing, target, ready, err := loadSourceAssetPromotion(ctx, reader, file, kind)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to reconcile source asset allocation", failurecause.Attr(err), "file_id", file.ID)
	}
	if err == nil && (ready || existing.Status == mediaasset.AssetAllocated) {
		return existing, target, ready, nil
	}
	return nil, nil, false, commitErr
}

func sourceAssetPromotionTarget(
	existing mediaasset.Asset,
	file File,
	kind string,
) (*mediaasset.WriteTarget, error) {
	if existing.Kind != kind || existing.Extension != file.Extension || existing.MimeType != file.MimeType ||
		existing.Disposition != "inline" || existing.DownloadFilename != nil {
		return nil, domainerr.Precondition("source file is already promoted with a different public asset contract")
	}
	expectedObjectKey, err := mediaauth.AssetObjectKey(existing.ID, existing.Extension)
	if err != nil || expectedObjectKey != existing.ObjectKey {
		return nil, domainerr.Precondition("source file public asset has a non-canonical object key").WithCause(failurecause.ServerFault(failurecause.WithAttrs(err, slog.String("asset_id", existing.ID))))
	}
	return &mediaasset.WriteTarget{
		AssetID:     existing.ID,
		ObjectKey:   existing.ObjectKey,
		Extension:   existing.Extension,
		MimeType:    existing.MimeType,
		Disposition: mediaasset.DispositionInline,
	}, nil
}

func (s *Service) failSourceAssetPromotion(ctx context.Context, assetID string, cause error) (bool, error) {
	var lastErr error
	for range 2 {
		now := time.Now().UTC()
		changed := false
		err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
			var err error
			changed, err = unit.Store().FailPromotionAsset(ctx, assetID, strings.TrimSpace(cause.Error()), now)
			return err
		})
		lastErr = err
		if err == nil && changed {
			return false, nil
		}
		var asset mediaasset.Asset
		if err := s.units.Read(ctx, func(store Store) error {
			var err error
			asset, err = store.PromotionAsset(ctx, assetID, false)
			return err
		}); err != nil {
			if lastErr != nil {
				return false, lastErr
			}
			return false, err
		}
		switch asset.Status {
		case mediaasset.AssetReady:
			return true, nil
		case mediaasset.AssetFailed:
			return false, nil
		case mediaasset.AssetAllocated:
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
	store Store,
	sourceFileID string,
	assetID string,
	result mediaasset.WrittenAsset,
) error {
	// File is the serialization authority for both new product references
	// and deletion. Keep the global File -> public_asset lock order.
	if _, err := store.PromotionFile(ctx, sourceFileID, true); err != nil {
		return err
	}
	asset, err := store.PromotionAsset(ctx, assetID, true)
	if err != nil {
		return err
	}
	if asset.SourceFileID == nil || *asset.SourceFileID != sourceFileID {
		return domainerr.Precondition("public asset no longer belongs to the source file")
	}
	if asset.Status == mediaasset.AssetFailed {
		now := time.Now().UTC()
		resumed, err := store.ResumePromotionAsset(ctx, assetID, false, now)
		if err != nil {
			return err
		}
		if !resumed {
			return domainerr.Precondition("failed public asset could not be resumed")
		}

	}
	return store.CompletePromotionAsset(ctx, s.cdnDomain, result)
}

func (s *Service) completeSourceAssetPromotion(
	ctx context.Context,
	sourceFileID string,
	assetID string,
	fileSize int64,
	digest []byte,
) error {
	result := mediaasset.WrittenAsset{AssetID: assetID, FileSize: fileSize, SHA256: append([]byte(nil), digest...)}
	var lastErr error
	for range 2 {
		callbackCompleted := false
		txErr := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
			store := unit.Store()
			if err := s.completeLockedSourceAsset(ctx, store, sourceFileID, assetID, result); err != nil {
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
			ctx, s.units, sourceFileID, assetID, fileSize, digest, txErr,
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
	reader fileStoreReader,
	sourceFileID string,
	assetID string,
	fileSize int64,
	digest []byte,
	commitErr error,
) (bool, error) {
	var file File
	if err := reader.Read(ctx, func(store Store) error {
		var err error
		file, err = store.PromotionFile(ctx, sourceFileID, false)
		return err
	}); err != nil || file.DeleteRequestedAt != nil {
		if err != nil {
			slog.ErrorContext(ctx, "Failed to reconcile source file after asset completion", failurecause.Attr(err), "file_id", sourceFileID, "asset_id", assetID)
		}
		return false, commitErr
	}
	var asset mediaasset.Asset
	if err := reader.Read(ctx, func(store Store) error {
		var err error
		asset, err = store.PromotionAsset(ctx, assetID, false)
		return err
	}); err != nil {
		slog.ErrorContext(ctx, "Failed to reconcile completed source asset", failurecause.Attr(err), "file_id", sourceFileID, "asset_id", assetID)
		return false, commitErr
	}
	if asset.SourceFileID == nil || *asset.SourceFileID != sourceFileID {
		return false, commitErr
	}
	switch asset.Status {
	case mediaasset.AssetReady:
		if asset.FileSize != nil && *asset.FileSize == fileSize && bytes.Equal(asset.SHA256, digest) {
			return true, nil
		}
		return false, domainerr.Precondition("public asset completion conflicts with ready metadata")
	case mediaasset.AssetAllocated, mediaasset.AssetFailed:
		return false, nil
	default:
		return false, commitErr
	}
}

func (s *Service) readySourcePromotionReference(ctx context.Context, id string) (*mediaasset.Reference, error) {
	var ref *mediaasset.Reference
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		ref, err = store.ReadyPromotionReference(ctx, s.cdnDomain, id)
		return err
	})
	return ref, err
}
