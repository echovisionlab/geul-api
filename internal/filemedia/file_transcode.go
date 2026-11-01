package filemedia

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// triggerFileScopedProcessingIfNeeded publishes stable derivative work from
// verified File authority. Document attachment is a separate revision-CAS
// mutation and never changes the processing identity.
func (s *Service) triggerFileScopedProcessingIfNeeded(ctx context.Context, fileID string) error {
	fileID = strings.TrimSpace(fileID)
	var file File
	if err := s.units.Read(ctx, func(store Store) error { var err error; file, err = store.IngestFile(ctx, fileID); return err }); err != nil {
		return fmt.Errorf("load File processing source: %w", err)
	}

	if file.DeleteRequestedAt != nil {
		return domainerr.Precondition("File is pending deletion")
	}

	if file.ClientMediaBundleID != nil {
		ready, err := s.clientMediaFileReady(ctx, file)
		if err != nil {
			return err
		}
		if !ready {
			return domainerr.Precondition("client media derivatives are not ready")
		}
		return nil
	}
	entityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE
	if err := s.units.Read(ctx, func(store Store) error { return store.RequireIngestTranscodePublisher(ctx) }); err != nil {
		return err
	}

	switch {
	case strings.HasPrefix(canonicalMimeType(file.MimeType), "audio/"):
		err := s.enqueueStableFileIngestTranscode(ctx, file, entityType, file.ID, domain.IngestTranscodeAudio)
		if err != nil {
			return fmt.Errorf("enqueue File audio transcode job: %w", err)
		}

	case strings.HasPrefix(canonicalMimeType(file.MimeType), "video/"):
		err := s.enqueueStableFileIngestTranscode(ctx, file, entityType, file.ID, domain.IngestTranscodeVideo)
		if err != nil {
			return fmt.Errorf("enqueue File video transcode job: %w", err)
		}
	}
	return nil
}

func (s *Service) promoteFileScopedImageIfNeeded(
	ctx context.Context,
	uploadType managev1.UploadType,
	slotID string,
	fileID string,
	mimeType string,
	current *mediaasset.Reference,
) (*mediaasset.Reference, error) {
	if current != nil || !strings.HasPrefix(canonicalMimeType(mimeType), "image/") {
		return current, nil
	}
	if _, dedicated := dedicatedPublicAssetKind(uploadType, slotID); dedicated {
		return current, nil
	}
	return s.promoteSourceFileToPublicAsset(ctx, fileID, "image")
}
