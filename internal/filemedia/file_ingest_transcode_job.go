package filemedia

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
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
	store Store,
	file File,
	entityType EntityType,
	entityID string,
) (*domain.IngestTranscodeCommand, bool, error) {
	source, err := domain.CanonicalObjectTarget(file)
	if err != nil {
		return nil, false, domainerr.Precondition("source file metadata is not canonical").WithCause(failurecause.ServerFault(failurecause.WithAttrs(err, slog.String("file_id", file.ID), slog.String("entity_type", entityType.String()), slog.String("entity_id", entityID))))
	}
	hls, spectrogram, shouldPublish, err := ensureStableFileIngestAudioOutputs(ctx, store, file.ID)
	if err != nil {
		return nil, false, err
	}
	return &domain.IngestTranscodeCommand{Kind: domain.IngestTranscodeAudio,
		EventID:        stableFileIngestFinalizerID(file.ID, "transcode-audio"),
		EntityType:     int32(entityType),
		EntityID:       strings.TrimSpace(entityID),
		FileID:         file.ID,
		Source:         source,
		HLSOutput:      hls,
		ArtifactOutput: spectrogram,
	}, shouldPublish, nil
}

func newStableFileIngestVideoTranscodeJob(
	ctx context.Context,
	store Store,
	file File,
	entityType EntityType,
	entityID string,
) (*domain.IngestTranscodeCommand, bool, error) {
	source, err := domain.CanonicalObjectTarget(file)
	if err != nil {
		return nil, false, domainerr.Precondition("source file metadata is not canonical").WithCause(failurecause.ServerFault(failurecause.WithAttrs(err, slog.String("file_id", file.ID), slog.String("entity_type", entityType.String()), slog.String("entity_id", entityID))))
	}
	hls, thumbnail, shouldPublish, err := ensureStableFileIngestVideoOutputs(ctx, store, file.ID)
	if err != nil {
		return nil, false, err
	}
	return &domain.IngestTranscodeCommand{Kind: domain.IngestTranscodeVideo,
		EventID:        stableFileIngestFinalizerID(file.ID, "transcode-video"),
		EntityType:     int32(entityType),
		EntityID:       strings.TrimSpace(entityID),
		FileID:         file.ID,
		Source:         source,
		HLSOutput:      hls,
		ArtifactOutput: thumbnail,
	}, shouldPublish, nil
}

func ensureStableFileIngestAudioOutputs(
	ctx context.Context,
	store Store,
	fileID string,
) (*domain.GenerationTarget, *mediaasset.WriteTarget, bool, error) {
	return ensureStableFileIngestOutputs(ctx, store, fileID, "spectrogram", "png", "image/png")
}

func ensureStableFileIngestVideoOutputs(
	ctx context.Context,
	store Store,
	fileID string,
) (*domain.GenerationTarget, *mediaasset.WriteTarget, bool, error) {
	return ensureStableFileIngestOutputs(ctx, store, fileID, "thumbnail", "webp", "image/webp")
}

func ensureStableFileIngestOutputs(
	ctx context.Context,
	store Store,
	fileID string,
	assetPurpose string,
	assetExtension string,
	assetMime string,
) (*domain.GenerationTarget, *mediaasset.WriteTarget, bool, error) {
	if err := store.LockIngestFile(ctx, fileID); err != nil {
		return nil, nil, false, err
	}
	hls, hlsAllocated, err := ensureStableFileIngestMediaGeneration(ctx, store, fileID)
	if err != nil {
		return nil, nil, false, err
	}
	asset, assetAllocated, err := ensureStableFileIngestPublicAsset(ctx, store, fileID, assetPurpose, assetExtension, assetMime)
	return hls, asset, hlsAllocated && assetAllocated, err
}

func ensureStableFileIngestMediaGeneration(
	ctx context.Context,
	store Store,
	fileID string,
) (*domain.GenerationTarget, bool, error) {
	generationID := stableFileIngestFinalizerID(fileID, "hls")
	objectPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	if err != nil {
		return nil, false, domainerr.InvalidField("file_id", err.Error())
	}
	now := time.Now().UTC()
	row := mediaasset.Generation{
		ID:           generationID,
		FileID:       fileID,
		Kind:         "hls",
		ObjectPrefix: objectPrefix,
		ManifestName: "master.m3u8",
		Status:       mediaasset.GenerationAllocated,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	row, err = store.EnsureIngestGeneration(ctx, row)
	if err != nil {
		return nil, false, err
	}

	if row.FileID != fileID || row.Kind != "hls" || row.ObjectPrefix != objectPrefix || row.ManifestName != "master.m3u8" {
		return nil, false, domainerr.Precondition("stable media generation allocation conflicts with existing domain state")
	}
	if row.Status == "" {
		return nil, false, domainerr.Precondition("stable media generation status is missing")
	}
	return &domain.GenerationTarget{
		GenerationID: row.ID,
		FileID:       row.FileID,
		ObjectPrefix: row.ObjectPrefix,
	}, row.Status == mediaasset.GenerationAllocated, nil
}

func ensureStableFileIngestPublicAsset(
	ctx context.Context,
	store Store,
	fileID string,
	kind string,
	extension string,
	mimeType string,
) (*mediaasset.WriteTarget, bool, error) {
	assetID := stableFileIngestFinalizerID(fileID, kind)
	objectKey, err := mediaauth.AssetObjectKey(assetID, extension)
	if err != nil {
		return nil, false, domainerr.InvalidField("extension", err.Error())
	}
	now := time.Now().UTC()
	row := mediaasset.Asset{
		ID:           assetID,
		SourceFileID: &fileID,
		Kind:         kind,
		ObjectKey:    objectKey,
		Extension:    extension,
		MimeType:     mimeType,
		Disposition:  "inline",
		Status:       mediaasset.AssetAllocated,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	row, err = store.EnsureIngestAsset(ctx, row)
	if err != nil {
		return nil, false, err
	}

	if row.SourceFileID == nil || *row.SourceFileID != fileID ||
		row.Kind != kind || row.ObjectKey != objectKey || row.Extension != extension ||
		row.MimeType != mimeType || row.Disposition != "inline" {
		return nil, false, domainerr.Precondition("stable public asset allocation conflicts with existing domain state")
	}
	if row.Status == "" {
		return nil, false, domainerr.Precondition("stable public asset status is missing")
	}
	return &mediaasset.WriteTarget{
		AssetID:     row.ID,
		ObjectKey:   row.ObjectKey,
		Extension:   row.Extension,
		MimeType:    row.MimeType,
		Disposition: mediaasset.DispositionInline,
	}, row.Status == mediaasset.AssetAllocated, nil
}
