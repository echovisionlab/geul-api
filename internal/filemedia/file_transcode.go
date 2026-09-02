package filemedia

import (
	"context"
	"fmt"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// triggerFileScopedProcessingIfNeeded publishes stable derivative work from
// verified File authority. Document attachment is a separate revision-CAS
// mutation and never changes the processing identity.
func (s *FileService) triggerFileScopedProcessingIfNeeded(ctx context.Context, fileID string) error {
	fileID = strings.TrimSpace(fileID)
	var file model.File
	if err := s.db.WithContext(ctx).Where("id = ?", fileID).Take(&file).Error; err != nil {
		return errs.Internal(fmt.Errorf("load File processing source: %w", err))
	}
	if file.DeleteRequestedAt != nil {
		return errs.FailedPrecondition("File is pending deletion")
	}

	entityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE
	registrar, err := s.fileIngestTranscodeJobRegistrar()
	if err != nil {
		return errs.Internal(err)
	}
	switch {
	case strings.HasPrefix(canonicalMimeType(file.MimeType), "audio/"):
		_, err = enqueueStableFileIngestAudioTranscodeJob(
			ctx,
			s.db,
			s.transcodeCommandPublisher(),
			registrar,
			file,
			entityType,
			file.ID,
		)
		if err != nil {
			return errs.Internal(fmt.Errorf("enqueue File audio transcode job: %w", err))
		}

	case strings.HasPrefix(canonicalMimeType(file.MimeType), "video/"):
		_, err = enqueueStableFileIngestVideoTranscodeJob(
			ctx,
			s.db,
			s.transcodeCommandPublisher(),
			registrar,
			file,
			entityType,
			file.ID,
		)
		if err != nil {
			return errs.Internal(fmt.Errorf("enqueue File video transcode job: %w", err))
		}
	}
	return nil
}

func (s *FileService) promoteFileScopedImageIfNeeded(
	ctx context.Context,
	uploadType managev1.UploadType,
	slotID string,
	fileID string,
	mimeType string,
	current *commonv1.AssetRef,
) (*commonv1.AssetRef, error) {
	if current != nil || !strings.HasPrefix(canonicalMimeType(mimeType), "image/") {
		return current, nil
	}
	if _, dedicated := dedicatedPublicAssetKind(uploadType, slotID); dedicated {
		return current, nil
	}
	return s.promoteSourceFileToPublicAsset(ctx, fileID, "image")
}

func CanonicalMediaObjectTargetForFile(file model.File) (*commonv1.MediaObjectTarget, error) {
	return meshMediaObjectTarget(file.ID, file.Extension, file.MimeType)
}
