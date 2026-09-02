package filemedia

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type storedFileDeliveryRow struct {
	ID              string  `gorm:"column:id"`
	Extension       string  `gorm:"column:extension"`
	MimeType        string  `gorm:"column:mime_type"`
	FileSize        int64   `gorm:"column:file_size"`
	DurationSeconds *int    `gorm:"column:duration_seconds"`
	FileName        *string `gorm:"column:file_name"`
	IngestSlotID    *string `gorm:"column:ingest_slot_id"`
	IngestAttemptID *string `gorm:"column:ingest_attempt_id"`
}

type storedDerivativeDeliveryRow struct {
	FileID                  string  `gorm:"column:file_id"`
	Type                    string  `gorm:"column:type"`
	AssetID                 *string `gorm:"column:asset_id"`
	AssetKind               *string `gorm:"column:asset_kind"`
	AssetExtension          *string `gorm:"column:asset_extension"`
	AssetMimeType           *string `gorm:"column:asset_mime_type"`
	AssetFileSize           *int64  `gorm:"column:asset_file_size"`
	AssetSHA256             []byte  `gorm:"column:asset_sha256"`
	AssetDisposition        *string `gorm:"column:asset_disposition"`
	AssetDownloadFilename   *string `gorm:"column:asset_download_filename"`
	AssetStatus             *string `gorm:"column:asset_status"`
	MediaGenerationID       *string `gorm:"column:media_generation_id"`
	MediaGenerationFileID   *string `gorm:"column:media_generation_file_id"`
	MediaGenerationManifest *string `gorm:"column:media_generation_manifest"`
	MediaGenerationStatus   *string `gorm:"column:media_generation_status"`
}

// resolveContentFileDeliveries preloads private inline refs for File Manager
// generated-output projection. The caller must apply the final File and exact
// generated-output relation fence before returning any ref.
func (s *FileService) resolveContentFileDeliveries(
	ctx context.Context,
	fileIDs []string,
) (map[string]*commonv1.MediaDelivery, error) {
	unique := make([]string, 0, len(fileIDs))
	seen := make(map[string]struct{}, len(fileIDs))
	for _, fileID := range fileIDs {
		fileID = strings.TrimSpace(fileID)
		if fileID == "" {
			continue
		}
		if _, exists := seen[fileID]; exists {
			continue
		}
		seen[fileID] = struct{}{}
		unique = append(unique, fileID)
	}
	result := make(map[string]*commonv1.MediaDelivery, len(unique))
	if len(unique) == 0 {
		return result, nil
	}

	var files []storedFileDeliveryRow
	if err := s.db.WithContext(ctx).
		Table("file").
		Select("id", "extension", "mime_type", "file_size", "duration_seconds", "file_name").
		Where("id IN ? AND delete_requested_at IS NULL", unique).
		Find(&files).Error; err != nil {
		return nil, errs.Internal(fmt.Errorf("load content file deliveries: %w", err))
	}
	for _, file := range files {
		inline, err := buildExpiringMediaFileRef(
			s.mediaDomain,
			s.mediaSecret,
			file.ID,
			file.Extension,
			file.MimeType,
			nil,
			mediaauth.PurposeInline,
			mediaauth.InlineTTL,
		)
		if err != nil {
			return nil, errs.Internal(err)
		}
		delivery := &commonv1.MediaDelivery{
			FileId:    file.ID,
			Extension: file.Extension,
			MimeType:  file.MimeType,
			FileSize:  file.FileSize,
			FileName:  file.FileName,
			Inline:    inline,
		}
		if file.DurationSeconds != nil {
			duration := int32(*file.DurationSeconds)
			delivery.DurationSeconds = &duration
		}
		result[file.ID] = delivery
	}
	if err := s.populateFileProcessingStatus(ctx, result); err != nil {
		slog.Warn("Failed to populate content file processing status", "error", err, "fileCount", len(result))
	}
	return result, nil
}

const MaxMediaDeliveryBatchSize = 100

// GetMediaDelivery returns delivery refs for a single file and its derivatives.
func (s *FileService) GetMediaDelivery(
	ctx context.Context,
	req *connect.Request[managev1.GetMediaDeliveryRequest],
) (*connect.Response[managev1.GetMediaDeliveryResponse], error) {
	return s.getMediaDelivery(ctx, req, true)
}

func (s *FileService) getMediaDelivery(
	ctx context.Context,
	req *connect.Request[managev1.GetMediaDeliveryRequest],
	allowChangedRetry bool,
) (*connect.Response[managev1.GetMediaDeliveryResponse], error) {
	if req.Msg.FileId == "" {
		return nil, errs.Required("file_id")
	}
	authorization, err := s.authorizeManageFileDeliveriesWithWitness(ctx, []string{req.Msg.FileId})
	if err != nil {
		return nil, err
	}

	response, err := s.getFileUrlsForID(ctx, req.Msg.FileId)
	if err != nil {
		return nil, err
	}
	if err := s.populateFileProcessingStatus(ctx, map[string]*commonv1.MediaDelivery{
		req.Msg.FileId: response.GetDelivery(),
	}); err != nil {
		slog.Warn("Failed to populate file processing status", "error", err, "fileId", req.Msg.FileId)
	}
	fenced, changed, err := s.finalizeManageFileURLResponses(ctx, map[string]*managev1.GetMediaDeliveryResponse{
		req.Msg.FileId: response,
	}, authorization)
	if err != nil {
		return nil, errs.Internal(err)
	}
	response = fenced[req.Msg.FileId]
	if response == nil {
		if changed && allowChangedRetry {
			retried, retryErr := s.getMediaDelivery(ctx, req, false)
			if retryErr == nil {
				return retried, nil
			}
			if connect.CodeOf(retryErr) != connect.CodePermissionDenied && connect.CodeOf(retryErr) != connect.CodeNotFound {
				return nil, retryErr
			}
		}
		return nil, errs.NotFound("file", req.Msg.FileId)
	}

	return connect.NewResponse(response), nil
}

// GetBulkMediaDeliveries returns delivery refs for multiple files and their derivatives.
func (s *FileService) GetBulkMediaDeliveries(
	ctx context.Context,
	req *connect.Request[managev1.GetBulkMediaDeliveriesRequest],
) (*connect.Response[managev1.GetBulkMediaDeliveriesResponse], error) {
	if len(req.Msg.FileIds) > MaxMediaDeliveryBatchSize {
		return nil, errs.InvalidArgument(
			"file_ids",
			fmt.Sprintf("must contain at most %d items", MaxMediaDeliveryBatchSize),
		)
	}
	if len(req.Msg.FileIds) == 0 {
		return connect.NewResponse(&managev1.GetBulkMediaDeliveriesResponse{
			Files: make(map[string]*managev1.GetMediaDeliveryResponse),
		}), nil
	}
	authorization, err := s.authorizeManageFileDeliveriesWithWitness(ctx, req.Msg.FileIds)
	if err != nil {
		return nil, err
	}
	result, err := s.loadFileURLResponses(ctx, req.Msg.FileIds)
	if err != nil {
		return nil, err
	}
	result, _, err = s.finalizeManageFileURLResponses(ctx, result, authorization)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.GetBulkMediaDeliveriesResponse{
		Files: result,
	}), nil
}

// loadFileURLResponses resolves immutable File IDs that an owning service has
// already authorized. It does not infer or re-read document attachment state.
func (s *FileService) loadFileURLResponses(
	ctx context.Context,
	fileIDs []string,
) (map[string]*managev1.GetMediaDeliveryResponse, error) {
	if len(fileIDs) == 0 {
		return map[string]*managev1.GetMediaDeliveryResponse{}, nil
	}
	var files []storedFileDeliveryRow
	if err := s.db.WithContext(ctx).
		Table("file").
		Select("id", "extension", "mime_type", "file_size", "duration_seconds", "file_name", "ingest_slot_id", "ingest_attempt_id").
		Where("id IN ? AND delete_requested_at IS NULL", fileIDs).
		Find(&files).Error; err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to query files: %w", err))
	}

	fileMap := make(map[string]storedFileDeliveryRow, len(files))
	resolvedFileIDs := make([]string, 0, len(files))
	for _, f := range files {
		fileMap[f.ID] = f
		resolvedFileIDs = append(resolvedFileIDs, f.ID)
	}

	derivatives, err := s.loadStoredDerivativeDeliveries(ctx, resolvedFileIDs)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to query file derivatives: %w", err))
	}

	derivativeMap := make(map[string]map[string]storedDerivativeDeliveryRow)
	for _, d := range derivatives {
		if _, ok := derivativeMap[d.FileID]; !ok {
			derivativeMap[d.FileID] = make(map[string]storedDerivativeDeliveryRow)
		}
		derivativeMap[d.FileID][d.Type] = d
	}
	imageFileIDs := make([]string, 0, len(files))
	meshFileIDs := make([]string, 0, len(files))
	for _, file := range files {
		switch {
		case strings.HasPrefix(normalizeMimeType(file.MimeType), "image/"):
			imageFileIDs = append(imageFileIDs, file.ID)
		case normalizeMimeType(file.MimeType) == "model/gltf-binary":
			meshFileIDs = append(meshFileIDs, file.ID)
		}
	}
	imageAssets, err := mediaasset.LoadReadyPublicAssetRefsForSourceFiles(ctx, s.db, s.cdnDomain, "image", imageFileIDs)
	if err != nil {
		return nil, err
	}
	meshAssets, err := mediaasset.LoadReadyPublicAssetRefsForSourceFiles(ctx, s.db, s.cdnDomain, "mesh", meshFileIDs)
	if err != nil {
		return nil, err
	}

	result := make(map[string]*managev1.GetMediaDeliveryResponse, len(fileIDs))
	deliveries := make(map[string]*commonv1.MediaDelivery, len(fileIDs))

	for _, fileID := range fileIDs {
		file, found := fileMap[fileID]
		if !found {
			continue
		}

		response, err := s.fileURLsResponseFromStoredFile(
			fileID,
			file.Extension,
			file.MimeType,
			file.FileSize,
			file.FileName,
		)
		if err != nil {
			return nil, errs.Internal(err)
		}
		if file.FileName != nil {
			response.Delivery.FileName = file.FileName
		}
		if file.DurationSeconds != nil {
			duration := int32(*file.DurationSeconds)
			response.Delivery.DurationSeconds = &duration
		}
		response.IngestSlotId = file.IngestSlotID
		response.IngestAttemptId = file.IngestAttemptID

		if derivs, ok := derivativeMap[fileID]; ok {
			if err := s.attachFileDerivativeURLs(response, fileID, derivs); err != nil {
				return nil, errs.Internal(err)
			}
		}
		if asset := imageAssets[fileID]; asset != nil {
			response.Delivery.Asset = asset
		} else if asset := meshAssets[fileID]; asset != nil {
			response.Delivery.Asset = asset
		}

		result[fileID] = response
		deliveries[fileID] = response.GetDelivery()
	}

	if err := s.populateFileProcessingStatus(ctx, deliveries); err != nil {
		slog.Warn("Failed to populate bulk file processing status", "error", err, "fileCount", len(result))
	}

	return result, nil
}

// getFileUrlsForID is a helper that returns delivery refs for a single file ID.
func (s *FileService) getFileUrlsForID(ctx context.Context, fileID string) (*managev1.GetMediaDeliveryResponse, error) {
	var file storedFileDeliveryRow
	if err := s.db.WithContext(ctx).
		Table("file").
		Select("id", "extension", "mime_type", "file_size", "duration_seconds", "file_name", "ingest_slot_id", "ingest_attempt_id").
		Where("id = ? AND delete_requested_at IS NULL", fileID).
		First(&file).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("file", fileID)
		}
		return nil, errs.Internal(fmt.Errorf("failed to query file: %w", err))
	}

	response, err := s.fileURLsResponseFromStoredFile(
		file.ID,
		file.Extension,
		file.MimeType,
		file.FileSize,
		file.FileName,
	)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if file.FileName != nil {
		response.Delivery.FileName = file.FileName
	}
	if file.DurationSeconds != nil {
		duration := int32(*file.DurationSeconds)
		response.Delivery.DurationSeconds = &duration
	}
	response.IngestSlotId = file.IngestSlotID
	response.IngestAttemptId = file.IngestAttemptID

	derivatives, err := s.loadStoredDerivativeDeliveries(ctx, []string{fileID})
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to query file derivatives: %w", err))
	}
	byType := make(map[string]storedDerivativeDeliveryRow, len(derivatives))
	for _, derivative := range derivatives {
		byType[derivative.Type] = derivative
	}
	if err := s.attachFileDerivativeURLs(response, fileID, byType); err != nil {
		return nil, errs.Internal(err)
	}

	return response, nil
}

func (s *FileService) loadStoredDerivativeDeliveries(
	ctx context.Context,
	fileIDs []string,
) ([]storedDerivativeDeliveryRow, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	var rows []storedDerivativeDeliveryRow
	err := s.db.WithContext(ctx).
		Table("file_derivative AS fd").
		Select(`
			fd.file_id,
			fd.type,
			pa.id AS asset_id,
			pa.kind AS asset_kind,
			pa.extension AS asset_extension,
			pa.mime_type AS asset_mime_type,
			pa.file_size AS asset_file_size,
			pa.sha256 AS asset_sha256,
			pa.disposition AS asset_disposition,
			pa.download_filename AS asset_download_filename,
			pa.status AS asset_status,
			mg.id AS media_generation_id,
			mg.file_id AS media_generation_file_id,
			mg.manifest_name AS media_generation_manifest,
			mg.status AS media_generation_status`).
		Joins("LEFT JOIN public_asset pa ON pa.id = fd.asset_id").
		Joins("LEFT JOIN media_generation mg ON mg.id = fd.media_generation_id").
		Where("fd.file_id IN ?", fileIDs).
		Find(&rows).Error
	return rows, err
}

func (s *FileService) fileURLsResponseFromStoredFile(
	fileID string,
	extension string,
	mimeType string,
	fileSize int64,
	fileName *string,
) (*managev1.GetMediaDeliveryResponse, error) {
	extension = strings.ToLower(strings.TrimSpace(extension))
	if extension == "" {
		return nil, fmt.Errorf("file %s has no canonical extension", fileID)
	}
	inline, err := buildExpiringMediaFileRef(
		s.mediaDomain, s.mediaSecret, fileID, extension, mimeType, nil, mediaauth.PurposeInline, mediaauth.InlineTTL,
	)
	if err != nil {
		return nil, err
	}
	download, err := buildExpiringMediaFileRef(
		s.mediaDomain, s.mediaSecret, fileID, extension, mimeType, fileName, mediaauth.PurposeDownload, s.effectiveDownloadTTL(),
	)
	if err != nil {
		return nil, err
	}
	return &managev1.GetMediaDeliveryResponse{
		Delivery: &commonv1.MediaDelivery{
			FileId:    fileID,
			Extension: extension,
			MimeType:  mimeType,
			FileSize:  fileSize,
			Inline:    inline,
			Download:  download,
		},
	}, nil
}

func buildExpiringMediaFileRef(
	mediaDomain string,
	mediaSecret string,
	fileID string,
	extension string,
	mimeType string,
	fileName *string,
	purpose mediaauth.Purpose,
	ttl time.Duration,
) (*commonv1.ExpiringMediaRef, error) {
	var deliveryPurpose commonv1.MediaDeliveryPurpose
	switch purpose {
	case mediaauth.PurposeInline:
		deliveryPurpose = commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_INLINE
	case mediaauth.PurposeDownload:
		deliveryPurpose = commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_DOWNLOAD
	default:
		return nil, fmt.Errorf("unsupported file delivery purpose %q", purpose)
	}
	var url string
	var err error
	var downloadFilename string
	if purpose == mediaauth.PurposeDownload {
		downloadFilename = CanonicalDownloadFilename(fileName, fileID, extension)
		url, err = BuildSignedMediaDownloadURL(
			mediaDomain,
			fileID,
			extension,
			mediaSecret,
			ttl,
			downloadFilename,
		)
	} else {
		url, err = BuildSignedMediaFileURL(mediaDomain, fileID, extension, mediaSecret, ttl, purpose)
	}
	if err != nil {
		return nil, err
	}
	ref := &commonv1.ExpiringMediaRef{
		FileId:    fileID,
		Url:       url,
		Purpose:   deliveryPurpose,
		ExpiresAt: timestamppb.New(time.Now().UTC().Add(ttl)),
		Extension: extension,
		MimeType:  mimeType,
		FileName:  normalizedOptionalString(fileName),
	}
	if purpose == mediaauth.PurposeDownload {
		ref.FileName = &downloadFilename
	}
	return ref, nil
}

func (s *FileService) attachFileDerivativeURLs(
	response *managev1.GetMediaDeliveryResponse,
	fileID string,
	derivs map[string]storedDerivativeDeliveryRow,
) error {
	if thumbnail, ok := derivs[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL.String()]; ok {
		ref, err := s.assetRefForDerivative(thumbnail)
		if err != nil {
			return err
		}
		response.Delivery.Thumbnail = ref
	}
	if hls, ok := derivs[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String()]; ok {
		ref, err := s.hlsPlaybackRef(fileID, hls)
		if err != nil {
			return err
		}
		response.Delivery.Playback = ref
	}
	if spectrogram, ok := derivs[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM.String()]; ok {
		ref, err := s.assetRefForDerivative(spectrogram)
		if err != nil {
			return err
		}
		response.Delivery.Spectrogram = ref
	}
	if waveform, ok := derivs[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String()]; ok {
		ref, err := s.assetRefForDerivative(waveform)
		if err != nil {
			return err
		}
		response.Delivery.Waveform = ref
	}
	return nil
}

func (s *FileService) assetRefForDerivative(row storedDerivativeDeliveryRow) (*commonv1.AssetRef, error) {
	if row.AssetID == nil || row.AssetStatus == nil || *row.AssetStatus != model.PublicAssetStatusReady {
		return nil, nil
	}
	if row.AssetExtension == nil || row.AssetMimeType == nil || row.AssetFileSize == nil || len(row.AssetSHA256) != 32 {
		return nil, fmt.Errorf("ready asset %s has incomplete metadata", *row.AssetID)
	}
	if (row.AssetKind != nil && *row.AssetKind == "attachment") ||
		row.AssetDisposition == nil || *row.AssetDisposition != "inline" {
		return nil, fmt.Errorf("attachment public assets cannot be emitted")
	}
	if row.AssetKind == nil {
		return nil, fmt.Errorf("ready asset %s has no canonical kind", *row.AssetID)
	}
	assetPath, err := mediaauth.AssetPath(*row.AssetID, *row.AssetKind, *row.AssetExtension)
	if err != nil {
		return nil, err
	}
	origin := s.cdnDomain
	if *row.AssetKind == "waveform" || *row.AssetKind == "spectrogram" {
		origin = s.mediaDomain
	}
	return &commonv1.AssetRef{
		AssetId:     *row.AssetID,
		Url:         joinOriginPath(origin, assetPath),
		Extension:   *row.AssetExtension,
		MimeType:    *row.AssetMimeType,
		FileSize:    *row.AssetFileSize,
		Sha256:      append([]byte(nil), row.AssetSHA256...),
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}, nil
}

func (s *FileService) hlsPlaybackRef(fileID string, row storedDerivativeDeliveryRow) (*commonv1.HlsMediaRef, error) {
	if row.MediaGenerationID == nil || row.MediaGenerationStatus == nil || *row.MediaGenerationStatus != model.MediaGenerationStatusReady {
		return nil, nil
	}
	if row.MediaGenerationFileID == nil || *row.MediaGenerationFileID != fileID {
		return nil, fmt.Errorf("media generation %s is not owned by file %s", *row.MediaGenerationID, fileID)
	}
	if row.MediaGenerationManifest == nil || strings.TrimSpace(*row.MediaGenerationManifest) == "" {
		return nil, fmt.Errorf("ready media generation %s has no manifest", *row.MediaGenerationID)
	}
	url, err := BuildPublicMediaHLSURL(s.mediaDomain, fileID, *row.MediaGenerationID, *row.MediaGenerationManifest)
	if err != nil {
		return nil, err
	}
	generationID := *row.MediaGenerationID
	return &commonv1.HlsMediaRef{
		FileId:       fileID,
		GenerationId: generationID,
		Url:          url,
	}, nil
}

func (s *FileService) effectiveDownloadTTL() time.Duration {
	if s != nil && s.downloadTTL > 0 {
		return min(s.downloadTTL, mediaauth.DownloadTTL)
	}
	return mediaauth.DownloadTTL
}
