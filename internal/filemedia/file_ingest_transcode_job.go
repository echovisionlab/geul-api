package filemedia

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

var fileIngestFinalizerNamespace = uuid.MustParse("ed84d962-d876-5d52-973c-9b5b4bdaf357")

func stableFileIngestFinalizerID(fileID string, purpose string) string {
	return uuid.NewSHA1(
		fileIngestFinalizerNamespace,
		[]byte(strings.TrimSpace(purpose)+":"+strings.TrimSpace(fileID)),
	).String()
}

func newStableFileIngestAudioTranscodeJob(
	ctx context.Context,
	db *gorm.DB,
	file model.File,
	entityType managev1.TranscodeEntityType,
	entityID string,
) (*managev1.TranscodeAudioEvent, bool, error) {
	source, err := CanonicalMediaObjectTargetForFile(file)
	if err != nil {
		return nil, false, errs.FailedPrecondition("source file metadata is not canonical")
	}
	hls, spectrogram, shouldPublish, err := ensureStableFileIngestAudioOutputs(ctx, db, file.ID)
	if err != nil {
		return nil, false, err
	}
	return &managev1.TranscodeAudioEvent{
		EventId:           stableFileIngestFinalizerID(file.ID, "transcode-audio"),
		EntityType:        entityType,
		EntityId:          strings.TrimSpace(entityID),
		FileId:            file.ID,
		Source:            source,
		HlsOutput:         hls,
		SpectrogramOutput: spectrogram,
	}, shouldPublish, nil
}

func newStableFileIngestVideoTranscodeJob(
	ctx context.Context,
	db *gorm.DB,
	file model.File,
	entityType managev1.TranscodeEntityType,
	entityID string,
) (*managev1.TranscodeVideoEvent, bool, error) {
	source, err := CanonicalMediaObjectTargetForFile(file)
	if err != nil {
		return nil, false, errs.FailedPrecondition("source file metadata is not canonical")
	}
	hls, thumbnail, shouldPublish, err := ensureStableFileIngestVideoOutputs(ctx, db, file.ID)
	if err != nil {
		return nil, false, err
	}
	return &managev1.TranscodeVideoEvent{
		EventId:         stableFileIngestFinalizerID(file.ID, "transcode-video"),
		EntityType:      entityType,
		EntityId:        strings.TrimSpace(entityID),
		FileId:          file.ID,
		Source:          source,
		HlsOutput:       hls,
		ThumbnailOutput: thumbnail,
	}, shouldPublish, nil
}

func ensureStableFileIngestAudioOutputs(
	ctx context.Context,
	db *gorm.DB,
	fileID string,
) (*commonv1.MediaGenerationWriteTarget, *commonv1.AssetWriteTarget, bool, error) {
	return ensureStableFileIngestOutputs(ctx, db, fileID, "spectrogram", "png", "image/png")
}

func ensureStableFileIngestVideoOutputs(
	ctx context.Context,
	db *gorm.DB,
	fileID string,
) (*commonv1.MediaGenerationWriteTarget, *commonv1.AssetWriteTarget, bool, error) {
	return ensureStableFileIngestOutputs(ctx, db, fileID, "thumbnail", "webp", "image/webp")
}

func ensureStableFileIngestOutputs(
	ctx context.Context,
	db *gorm.DB,
	fileID string,
	assetPurpose string,
	assetExtension string,
	assetMime string,
) (*commonv1.MediaGenerationWriteTarget, *commonv1.AssetWriteTarget, bool, error) {
	var hls *commonv1.MediaGenerationWriteTarget
	var asset *commonv1.AssetWriteTarget
	var hlsAllocated, assetAllocated bool
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := mediaasset.LockAttachableFilesForUpdate(ctx, tx, []string{fileID}); err != nil {
			return err
		}
		var err error
		hls, hlsAllocated, err = ensureStableFileIngestMediaGeneration(ctx, tx, fileID)
		if err != nil {
			return err
		}
		asset, assetAllocated, err = ensureStableFileIngestPublicAsset(
			ctx,
			tx,
			fileID,
			assetPurpose,
			assetExtension,
			assetMime,
		)
		return err
	})
	return hls, asset, hlsAllocated && assetAllocated, err
}

func ensureStableFileIngestMediaGeneration(
	ctx context.Context,
	db *gorm.DB,
	fileID string,
) (*commonv1.MediaGenerationWriteTarget, bool, error) {
	generationID := stableFileIngestFinalizerID(fileID, "hls")
	objectPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	if err != nil {
		return nil, false, errs.InvalidArgument("file_id", err.Error())
	}
	now := time.Now().UTC()
	row := model.MediaGeneration{
		ID:           generationID,
		FileID:       fileID,
		Kind:         "hls",
		ObjectPrefix: objectPrefix,
		ManifestName: "master.m3u8",
		Status:       model.MediaGenerationStatusAllocated,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, false, errs.Internal(err)
	}
	if err := db.WithContext(ctx).Where("id = ?", generationID).Take(&row).Error; err != nil {
		return nil, false, errs.Internal(err)
	}
	if row.FileID != fileID || row.Kind != "hls" || row.ObjectPrefix != objectPrefix || row.ManifestName != "master.m3u8" {
		return nil, false, errs.FailedPrecondition("stable media generation allocation conflicts with existing domain state")
	}
	if row.Status == "" {
		return nil, false, errs.FailedPrecondition("stable media generation status is missing")
	}
	return &commonv1.MediaGenerationWriteTarget{
		GenerationId: row.ID,
		FileId:       row.FileID,
		ObjectPrefix: row.ObjectPrefix,
	}, row.Status == model.MediaGenerationStatusAllocated, nil
}

func ensureStableFileIngestPublicAsset(
	ctx context.Context,
	db *gorm.DB,
	fileID string,
	kind string,
	extension string,
	mimeType string,
) (*commonv1.AssetWriteTarget, bool, error) {
	assetID := stableFileIngestFinalizerID(fileID, kind)
	objectKey, err := mediaauth.AssetObjectKey(assetID, extension)
	if err != nil {
		return nil, false, errs.InvalidArgument("extension", err.Error())
	}
	now := time.Now().UTC()
	row := model.PublicAsset{
		ID:           assetID,
		SourceFileID: &fileID,
		Kind:         kind,
		ObjectKey:    objectKey,
		Extension:    extension,
		MimeType:     mimeType,
		Disposition:  "inline",
		Status:       model.PublicAssetStatusAllocated,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, false, errs.Internal(err)
	}
	if err := db.WithContext(ctx).Where("id = ?", assetID).Take(&row).Error; err != nil {
		return nil, false, errs.Internal(err)
	}
	if row.SourceFileID == nil || *row.SourceFileID != fileID ||
		row.Kind != kind || row.ObjectKey != objectKey || row.Extension != extension ||
		row.MimeType != mimeType || row.Disposition != "inline" {
		return nil, false, errs.FailedPrecondition("stable public asset allocation conflicts with existing domain state")
	}
	if row.Status == "" {
		return nil, false, errs.FailedPrecondition("stable public asset status is missing")
	}
	return &commonv1.AssetWriteTarget{
		AssetId:     row.ID,
		ObjectKey:   row.ObjectKey,
		Extension:   row.Extension,
		MimeType:    row.MimeType,
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}, row.Status == model.PublicAssetStatusAllocated, nil
}
