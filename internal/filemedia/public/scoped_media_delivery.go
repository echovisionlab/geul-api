package public

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"gorm.io/gorm"
)

const maxDirectDraftMediaTTL = 5 * time.Minute

func directDraftMediaTTL(configured time.Duration) time.Duration {
	if configured <= 0 || configured > maxDirectDraftMediaTTL {
		return maxDirectDraftMediaTTL
	}
	return configured
}

type scopedMediaFile struct {
	ID        string  `gorm:"column:id"`
	Extension string  `gorm:"column:extension"`
	MimeType  string  `gorm:"column:mime_type"`
	FileSize  int64   `gorm:"column:file_size"`
	FileName  *string `gorm:"column:file_name"`
}

type scopedMediaDerivative struct {
	FileID            string  `gorm:"column:file_id"`
	Type              string  `gorm:"column:type"`
	AssetID           *string `gorm:"column:asset_id"`
	MediaGenerationID *string `gorm:"column:media_generation_id"`
}

func (s *FileService) buildScopedMediaURLsWithDB(
	ctx context.Context,
	db *gorm.DB,
	accessByFileID map[string]resolvedMediaAccess,
) (map[string]*commonv1.MediaDelivery, error) {
	allowedFileIDs := make([]string, 0, len(accessByFileID))
	for fileID := range accessByFileID {
		allowedFileIDs = append(allowedFileIDs, fileID)
	}
	slices.Sort(allowedFileIDs)
	if len(allowedFileIDs) == 0 {
		return map[string]*commonv1.MediaDelivery{}, nil
	}

	var files []scopedMediaFile
	if err := db.WithContext(ctx).
		Table("file").
		Select("id, extension, mime_type, file_size, file_name").
		Where("id IN ? AND delete_requested_at IS NULL", allowedFileIDs).
		Find(&files).Error; err != nil {
		return nil, errs.Internal(err)
	}

	fileMap := make(map[string]scopedMediaFile, len(files))
	for _, file := range files {
		fileMap[file.ID] = file
	}

	var derivatives []scopedMediaDerivative
	if err := db.WithContext(ctx).
		Table("file_derivative").
		Select("file_id, type, asset_id, media_generation_id").
		Where("file_id IN ?", allowedFileIDs).
		Find(&derivatives).Error; err != nil {
		return nil, errs.Internal(err)
	}
	assetIDs := make([]string, 0, len(derivatives))
	generationIDs := make([]string, 0, len(derivatives))
	for _, derivative := range derivatives {
		if derivative.AssetID != nil {
			assetIDs = append(assetIDs, *derivative.AssetID)
		}
		if derivative.MediaGenerationID != nil {
			generationIDs = append(generationIDs, *derivative.MediaGenerationID)
		}
	}
	assetsByID, err := loadReadyPublicAssetsByIDs(ctx, db, assetIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	generationsByID, err := loadReadyMediaGenerationsByIDs(ctx, db, generationIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	publicAssetsByFileID, err := loadReadyPublicAssetsForSourceFiles(
		ctx,
		db,
		allowedFileIDs,
		"image", "mesh", "texture", "gallery", "artwork", "poster", "map_image",
	)
	if err != nil {
		return nil, errs.Internal(err)
	}

	derivativeMap := make(map[string]map[string]scopedMediaDerivative, len(allowedFileIDs))
	for _, derivative := range derivatives {
		if _, ok := derivativeMap[derivative.FileID]; !ok {
			derivativeMap[derivative.FileID] = make(map[string]scopedMediaDerivative)
		}
		derivativeMap[derivative.FileID][derivative.Type] = derivative
	}

	result := make(map[string]*commonv1.MediaDelivery, len(allowedFileIDs))
	for _, fileID := range allowedFileIDs {
		file, found := fileMap[fileID]
		if !found {
			continue
		}

		urls, err := projectScopedMediaURL(
			s.cdnDomain,
			s.mediaDomain,
			s.mediaSecret,
			accessByFileID[fileID],
			file,
			derivativeMap[fileID],
			assetsByID,
			generationsByID,
			publicAssetsByFileID[fileID],
		)
		if err != nil {
			return nil, errs.Internal(err)
		}
		result[fileID] = urls
	}

	return result, nil
}

// ResolvePublicDisplayMedia projects only immutable ready public image assets
// after the owning public domain has authorized the exact featured-image
// binding. Missing or failed projections remain unavailable; this path never
// signs the private original File.
func (s *FileService) ResolvePublicDisplayMedia(
	ctx context.Context,
	fileIDs []string,
) (map[string]*commonv1.MediaDelivery, error) {
	requested := make([]string, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		fileID = strings.TrimSpace(fileID)
		if fileID != "" {
			requested = append(requested, fileID)
		}
	}
	requested = uniqueNonEmptyIDs(requested)
	result := make(map[string]*commonv1.MediaDelivery, len(requested))
	if len(requested) == 0 {
		return result, nil
	}

	var files []scopedMediaFile
	if err := s.db.WithContext(ctx).Table("file").
		Select("id, extension, mime_type, file_size, file_name").
		Where("id IN ? AND delete_requested_at IS NULL", requested).
		Find(&files).Error; err != nil {
		return nil, errs.Internal(err)
	}
	assets, err := loadReadyPublicAssetsForSourceFiles(
		ctx, s.db, requested, "image", "gallery", "artwork", "poster", "map_image",
	)
	if err != nil {
		return nil, errs.Internal(err)
	}
	for _, file := range files {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(file.MimeType)), "image/") {
			continue
		}
		asset, found := assets[file.ID]
		if !found || asset.SourceFileID == nil || *asset.SourceFileID != file.ID {
			continue
		}
		ref, projectErr := projectReadyPublicAsset(s.cdnDomain, asset)
		if projectErr != nil {
			return nil, errs.Internal(projectErr)
		}
		delivery := &commonv1.MediaDelivery{
			FileId: file.ID, Extension: file.Extension, MimeType: file.MimeType,
			FileSize: file.FileSize, Asset: ref, Thumbnail: ref,
		}
		if file.FileName != nil {
			delivery.FileName = file.FileName
		}
		result[file.ID] = delivery
	}
	return result, nil
}

func projectScopedMediaURL(
	cdnDomain string,
	mediaDomain string,
	mediaSecret string,
	access resolvedMediaAccess,
	file scopedMediaFile,
	derivatives map[string]scopedMediaDerivative,
	assetsByID map[string]readyPublicAssetRow,
	generationsByID map[string]readyMediaGenerationRow,
	publicAsset readyPublicAssetRow,
) (*commonv1.MediaDelivery, error) {
	if expected := model.GetExtensionFromMime(file.MimeType); expected == "bin" || expected != file.Extension {
		return nil, errors.New("file extension does not match MIME type")
	}
	response := &commonv1.MediaDelivery{
		FileId:    file.ID,
		Extension: file.Extension,
		MimeType:  file.MimeType,
		FileSize:  file.FileSize,
	}
	if file.FileName != nil {
		response.FileName = file.FileName
	}

	if publicAsset.AssetID != "" {
		assetRef, err := projectReadyPublicAsset(cdnDomain, publicAsset)
		if err != nil {
			return nil, err
		}
		response.Asset = assetRef
		if strings.HasPrefix(strings.ToLower(file.MimeType), "image/") {
			response.Thumbnail = assetRef
		}
	}

	if response.Asset == nil && publicMediaSupportsInlineSource(file.MimeType) {
		inlineTTL := mediaauth.InlineTTL
		if access.directDraft {
			inlineTTL = directDraftMediaTTL(inlineTTL)
		}
		inlineRef, err := buildExpiringFileRef(
			mediaDomain,
			mediaSecret,
			file.ID,
			file.Extension,
			file.MimeType,
			file.FileName,
			mediaauth.PurposeInline,
			inlineTTL,
		)
		if err != nil {
			return nil, err
		}
		response.Inline = inlineRef
	}

	thumbnail, err := projectDerivativeAsset(
		cdnDomain, derivatives, assetsByID,
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL,
		true,
	)
	if err != nil {
		return nil, err
	}
	if thumbnail != nil {
		response.Thumbnail = thumbnail
	}
	response.Playback, err = projectHLSPlayback(mediaDomain, file.ID, derivatives, generationsByID)
	if err != nil {
		return nil, err
	}
	response.Spectrogram, err = projectDerivativeAsset(
		mediaDomain, derivatives, assetsByID,
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM,
		true,
	)
	if err != nil {
		return nil, err
	}
	response.Waveform, err = projectDerivativeAsset(
		mediaDomain, derivatives, assetsByID,
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM,
		true,
	)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func projectDerivativeAsset(
	cdnDomain string,
	derivatives map[string]scopedMediaDerivative,
	assetsByID map[string]readyPublicAssetRow,
	derivativeType managev1.FileDerivativeType,
	allowed bool,
) (*commonv1.AssetRef, error) {
	if !allowed {
		return nil, nil
	}
	derivative, found := derivatives[derivativeType.String()]
	if !found || derivative.AssetID == nil {
		return nil, nil
	}
	asset, found := assetsByID[*derivative.AssetID]
	if !found {
		return nil, nil
	}
	return projectReadyPublicAsset(cdnDomain, asset)
}

func projectHLSPlayback(
	mediaDomain string,
	fileID string,
	derivatives map[string]scopedMediaDerivative,
	generationsByID map[string]readyMediaGenerationRow,
) (*commonv1.HlsMediaRef, error) {
	derivative, found := derivatives[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String()]
	if !found || derivative.MediaGenerationID == nil {
		return nil, nil
	}
	generation, found := generationsByID[*derivative.MediaGenerationID]
	if !found {
		return nil, nil
	}
	if generation.FileID != fileID {
		return nil, errors.New("media generation does not belong to requested file")
	}
	return buildPublicHLSRef(mediaDomain, generation)
}

func publicMediaSupportsInlineSource(mimeType string) bool {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	return strings.HasPrefix(mimeType, "image/") || mimeType == "model/gltf-binary"
}
