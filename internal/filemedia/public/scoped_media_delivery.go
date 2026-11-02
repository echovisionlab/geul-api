package public

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediamime"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
)

const maxDirectDraftMediaTTL = 5 * time.Minute

func directDraftMediaTTL(configured time.Duration) time.Duration {
	if configured <= 0 || configured > maxDirectDraftMediaTTL {
		return maxDirectDraftMediaTTL
	}
	return configured
}

func (s *Service) buildScopedMediaURLs(
	ctx context.Context,
	store Store,
	accessByFileID map[string]resolvedMediaAccess,
) (map[string]*Delivery, error) {
	allowedFileIDs := make([]string, 0, len(accessByFileID))
	for fileID := range accessByFileID {
		allowedFileIDs = append(allowedFileIDs, fileID)
	}
	slices.Sort(allowedFileIDs)
	if len(allowedFileIDs) == 0 {
		return map[string]*Delivery{}, nil
	}

	files, err := store.MediaFiles(ctx, allowedFileIDs)
	if err != nil {
		return nil, err
	}

	fileMap := make(map[string]MediaFile, len(files))
	for _, file := range files {
		fileMap[file.ID] = file
	}

	derivatives, err := store.Derivatives(ctx, allowedFileIDs)
	if err != nil {
		return nil, err
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
	assetsByID, err := store.ReadyAssets(ctx, assetIDs)
	if err != nil {
		return nil, err
	}
	generationsByID, err := store.ReadyGenerations(ctx, generationIDs)
	if err != nil {
		return nil, err
	}
	publicAssetsByFileID, err := store.ReadyAssetsForFiles(
		ctx,
		allowedFileIDs,
		"image", "mesh", "texture", "gallery", "artwork", "poster", "map_image",
	)
	if err != nil {
		return nil, err
	}

	derivativeMap := make(map[string]map[string]Derivative, len(allowedFileIDs))
	for _, derivative := range derivatives {
		if _, ok := derivativeMap[derivative.FileID]; !ok {
			derivativeMap[derivative.FileID] = make(map[string]Derivative)
		}
		derivativeMap[derivative.FileID][derivative.Type] = derivative
	}

	result := make(map[string]*Delivery, len(allowedFileIDs))
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
			return nil, err
		}
		result[fileID] = urls
	}

	return result, nil
}

// ResolveDisplayMedia projects only immutable ready public image assets
// after the owning public domain has authorized the exact featured-image
// binding. Missing or failed projections remain unavailable; this path never
// signs the private original File.
func (s *Service) ResolveDisplayMedia(
	ctx context.Context,
	fileIDs []string,
) (map[string]*Delivery, error) {
	requested := make([]string, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		fileID = strings.TrimSpace(fileID)
		if fileID != "" {
			requested = append(requested, fileID)
		}
	}
	requested = sliceutil.UniqueTrimmed(requested)
	result := make(map[string]*Delivery, len(requested))
	if len(requested) == 0 {
		return result, nil
	}

	err := s.units.Read(ctx, func(store Store) error {
		files, err := store.MediaFiles(ctx, requested)
		if err != nil {
			return err
		}
		assets, err := store.ReadyAssetsForFiles(ctx, requested, "image", "gallery", "artwork", "poster", "map_image")
		if err != nil {
			return err
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
				return projectErr
			}
			delivery := &Delivery{
				FileID: file.ID, Extension: file.Extension, MimeType: file.MimeType,
				FileSize: file.FileSize, Asset: ref, Thumbnail: ref,
			}
			if file.FileName != nil {
				delivery.FileName = file.FileName
			}
			result[file.ID] = delivery
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func projectScopedMediaURL(
	cdnDomain string,
	mediaDomain string,
	mediaSecret string,
	access resolvedMediaAccess,
	file MediaFile,
	derivatives map[string]Derivative,
	assetsByID map[string]ReadyAsset,
	generationsByID map[string]ReadyGeneration,
	publicAsset ReadyAsset,
) (*Delivery, error) {
	if expected := mediamime.GetExtensionFromMime(file.MimeType); expected == "bin" || expected != file.Extension {
		return nil, errors.New("file extension does not match MIME type")
	}
	response := &Delivery{
		FileID:    file.ID,
		Extension: file.Extension,
		MimeType:  file.MimeType,
		FileSize:  file.FileSize,
	}
	if file.FileName != nil {
		response.FileName = file.FileName
	}
	if file.DurationSeconds != nil {
		duration := int32(*file.DurationSeconds)
		response.DurationSeconds = &duration
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
		inlineRef, err := mediaauth.ExpiringReference(
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
		derivativeThumbnail,
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
		derivativeSpectrogram,
		true,
	)
	if err != nil {
		return nil, err
	}
	response.Waveform, err = projectDerivativeAsset(
		mediaDomain, derivatives, assetsByID,
		derivativeWaveform,
		true,
	)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func projectDerivativeAsset(
	cdnDomain string,
	derivatives map[string]Derivative,
	assetsByID map[string]ReadyAsset,
	derivativeType string,
	allowed bool,
) (*mediaasset.Reference, error) {
	if !allowed {
		return nil, nil
	}
	derivative, found := derivatives[derivativeType]
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
	derivatives map[string]Derivative,
	generationsByID map[string]ReadyGeneration,
) (*HLSReference, error) {
	derivative, found := derivatives[derivativeHls]
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
