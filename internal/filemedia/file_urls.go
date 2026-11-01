package filemedia

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type storedFileDeliveryRow = domain.StoredFileDelivery
type storedDerivativeDeliveryRow = domain.StoredDerivativeDelivery

// resolveContentFileDeliveries preloads private inline refs for File Manager
// generated-output projection. The caller must apply the final File and exact
// generated-output relation fence before returning any ref.
func (s *Service) resolveContentFileDeliveries(
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
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		files, err = store.StoredFileDeliveries(ctx, unique, false)
		return err
	}); err != nil {
		return nil, fmt.Errorf("load content file deliveries: %w", err)
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
			return nil, err
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
		slog.ErrorContext(ctx, "Failed to populate content file processing status", failurecause.Attr(err), "fileCount", len(result))
	}
	return result, nil
}

const MaxMediaDeliveryBatchSize = 100

// GetMediaDeliveryQuery reads the deliveries of one File.
type GetMediaDeliveryQuery struct {
	FileID string
}

// GetMediaDelivery returns delivery refs for a single file and its derivatives.
func (s *Service) GetMediaDelivery(ctx context.Context, query GetMediaDeliveryQuery) (*managev1.GetMediaDeliveryResponse, error) {
	return s.getMediaDelivery(ctx, query.FileID, true)
}

func (s *Service) getMediaDelivery(ctx context.Context, fileID string, allowChangedRetry bool) (*managev1.GetMediaDeliveryResponse, error) {
	if fileID == "" {
		return nil, domainerr.Required("file_id")
	}
	authorization, err := s.authorizeManageFileDeliveriesWithWitness(ctx, []string{fileID})
	if err != nil {
		return nil, err
	}

	response, err := s.getFileUrlsForID(ctx, fileID)
	if err != nil {
		return nil, err
	}
	fenced, changed, err := s.finalizeManageFileURLResponses(ctx, map[string]*managev1.GetMediaDeliveryResponse{
		fileID: response,
	}, authorization)
	if err != nil {
		return nil, err
	}
	response = fenced[fileID]
	if response == nil {
		if changed && allowChangedRetry {
			retried, retryErr := s.getMediaDelivery(ctx, fileID, false)
			if retryErr == nil {
				return retried, nil
			}
			if !mutation.IsDenied(retryErr) && !mutation.IsNotFound(retryErr) {
				return nil, retryErr
			}
		}
		return nil, domainerr.ResourceNotFound("file", fileID)
	}
	return response, nil
}

// GetBulkMediaDeliveriesQuery reads the deliveries of several Files.
type GetBulkMediaDeliveriesQuery struct {
	FileIDs []string
}

// GetBulkMediaDeliveries returns delivery refs for multiple files and their
// derivatives, keyed by File ID.
func (s *Service) GetBulkMediaDeliveries(ctx context.Context, query GetBulkMediaDeliveriesQuery) (map[string]*managev1.GetMediaDeliveryResponse, error) {
	if len(query.FileIDs) > MaxMediaDeliveryBatchSize {
		return nil, domainerr.InvalidField("file_ids", fmt.Sprintf("must contain at most %d items", MaxMediaDeliveryBatchSize))
	}
	if len(query.FileIDs) == 0 {
		return make(map[string]*managev1.GetMediaDeliveryResponse), nil
	}
	authorization, err := s.authorizeManageFileDeliveriesWithWitness(ctx, query.FileIDs)
	if err != nil {
		return nil, err
	}
	result, err := s.loadFileURLResponses(ctx, query.FileIDs)
	if err != nil {
		return nil, err
	}
	result, _, err = s.finalizeManageFileURLResponses(ctx, result, authorization)
	return result, err
}

// loadFileURLResponses resolves immutable File IDs that an owning service has
// already authorized. It does not infer or re-read document attachment state.
func (s *Service) loadFileURLResponses(
	ctx context.Context,
	fileIDs []string,
) (map[string]*managev1.GetMediaDeliveryResponse, error) {
	return s.loadFileURLResponsesWithReader(ctx, s.units, fileIDs)
}
func (s *Service) loadFileURLResponsesWithReader(ctx context.Context, reader fileDeliveryReader, fileIDs []string) (map[string]*managev1.GetMediaDeliveryResponse, error) {
	if len(fileIDs) == 0 {
		return map[string]*managev1.GetMediaDeliveryResponse{}, nil
	}
	var files []storedFileDeliveryRow
	if err := reader.Read(ctx, func(store Store) error {
		var err error
		files, err = store.StoredFileDeliveries(ctx, fileIDs, true)
		return err
	}); err != nil {
		return nil, fmt.Errorf("failed to query files: %w", err)
	}

	fileMap := make(map[string]storedFileDeliveryRow, len(files))
	resolvedFileIDs := make([]string, 0, len(files))
	for _, f := range files {
		fileMap[f.ID] = f
		resolvedFileIDs = append(resolvedFileIDs, f.ID)
	}

	derivatives, err := s.loadStoredDerivativeDeliveries(ctx, reader, resolvedFileIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to query file derivatives: %w", err)
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
	imageAssets, err := mediaasset.ReferenceMapResultProto(s.sourceAssetReferencesWithReader(ctx, reader, "image", imageFileIDs))
	if err != nil {
		return nil, err
	}
	meshAssets, err := mediaasset.ReferenceMapResultProto(s.sourceAssetReferencesWithReader(ctx, reader, "mesh", meshFileIDs))
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
			return nil, err
		}
		response.ClientMediaBundleId = file.ClientMediaBundleID
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
				return nil, err
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

	if err := s.populateFileProcessingStatusWithReader(ctx, reader, deliveries); err != nil {
		slog.ErrorContext(ctx, "Failed to populate bulk file processing status", failurecause.Attr(err), "fileCount", len(result))
	}

	return result, nil
}

// getFileUrlsForID is a helper that returns delivery refs for a single file ID.
func (s *Service) getFileUrlsForID(ctx context.Context, fileID string) (*managev1.GetMediaDeliveryResponse, error) {
	responses, err := s.loadFileURLResponses(ctx, []string{fileID})
	if err != nil {
		return nil, err
	}
	response := responses[fileID]
	if response == nil {
		return nil, domainerr.ResourceNotFound("file", fileID)
	}
	return response, nil
}

func (s *Service) loadStoredDerivativeDeliveries(
	ctx context.Context,
	reader fileDeliveryReader,
	fileIDs []string,
) ([]storedDerivativeDeliveryRow, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	var rows []storedDerivativeDeliveryRow
	err := reader.Read(ctx, func(store Store) error {
		var err error
		rows, err = store.StoredDerivativeDeliveries(ctx, fileIDs)
		return err
	})
	return rows, err
}

func (s *Service) fileURLsResponseFromStoredFile(
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
	var downloadFilename string
	if purpose == mediaauth.PurposeDownload {
		downloadFilename = mediaauth.CanonicalDownloadFilename(fileName, fileID, extension)
	}
	url, expiresAt, err := mediaauth.SignFileURL(
		mediaDomain,
		fileID,
		extension,
		mediaSecret,
		ttl,
		purpose,
		downloadFilename,
	)
	if err != nil {
		return nil, err
	}
	ref := &commonv1.ExpiringMediaRef{
		FileId:    fileID,
		Url:       url,
		Purpose:   deliveryPurpose,
		ExpiresAt: timestamppb.New(expiresAt),
		Extension: extension,
		MimeType:  mimeType,
		FileName:  normalizedOptionalString(fileName),
	}
	if purpose == mediaauth.PurposeDownload {
		ref.FileName = &downloadFilename
	}
	return ref, nil
}

func (s *Service) attachFileDerivativeURLs(
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

func (s *Service) assetRefForDerivative(row storedDerivativeDeliveryRow) (*commonv1.AssetRef, error) {
	if row.AssetID == nil || row.AssetStatus == nil || *row.AssetStatus != string(mediaasset.AssetReady) {
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
		Url:         mediaauth.JoinOrigin(origin, assetPath),
		Extension:   *row.AssetExtension,
		MimeType:    *row.AssetMimeType,
		FileSize:    *row.AssetFileSize,
		Sha256:      append([]byte(nil), row.AssetSHA256...),
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}, nil
}

func (s *Service) hlsPlaybackRef(fileID string, row storedDerivativeDeliveryRow) (*commonv1.HlsMediaRef, error) {
	if row.MediaGenerationID == nil || row.MediaGenerationStatus == nil || *row.MediaGenerationStatus != string(mediaasset.GenerationReady) {
		return nil, nil
	}
	if row.MediaGenerationFileID == nil || *row.MediaGenerationFileID != fileID {
		return nil, fmt.Errorf("media generation %s is not owned by file %s", *row.MediaGenerationID, fileID)
	}
	if row.MediaGenerationManifest == nil || strings.TrimSpace(*row.MediaGenerationManifest) == "" {
		return nil, fmt.Errorf("ready media generation %s has no manifest", *row.MediaGenerationID)
	}
	url, err := mediaauth.PublicHLSURL(s.mediaDomain, fileID, *row.MediaGenerationID, *row.MediaGenerationManifest)
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

func (s *Service) effectiveDownloadTTL() time.Duration {
	if s != nil && s.downloadTTL > 0 {
		return min(s.downloadTTL, mediaauth.DownloadTTL)
	}
	return mediaauth.DownloadTTL
}

func (s *Service) sourceAssetReferencesWithReader(ctx context.Context, reader fileDeliveryReader, kind string, ids []string) (map[string]*mediaasset.Reference, error) {
	if len(ids) == 0 {
		return map[string]*mediaasset.Reference{}, nil
	}
	var refs map[string]*mediaasset.Reference
	err := reader.Read(ctx, func(store Store) error {
		var err error
		refs, err = store.SourceAssetReferences(ctx, s.cdnDomain, kind, ids)
		return err
	})
	return refs, err
}

type fileDeliveryReader interface {
	Read(context.Context, func(Store) error) error
}
type boundDeliveryReader struct{ Store }

func (r boundDeliveryReader) Read(_ context.Context, read func(Store) error) error {
	return read(r.Store)
}
