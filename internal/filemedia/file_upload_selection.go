package filemedia

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

func (s *Service) findActiveUploadSessionsForSurface(ctx context.Context, uploadType UploadType, entityID string, entityType *string, target fileIngestProjectionIdentity) ([]domain.UploadSession, error) {
	if uploadType == UploadGeneralFile || isEditorFileIngestUploadType(uploadType) || target.mode == fileIngestTargetModeUnknown || (target.mode == fileIngestTargetModeGeneral && target.slotID == "") {
		return nil, nil
	}
	filter := domain.UploadSearch{UploadType: uploadType.String(), EntityID: entityID, EntityType: entityType, Mode: target.mode, SlotID: target.slotID, ExpectedCurrentFileID: target.expectedCurrentFileID, ActiveAfter: time.Now().Add(-multipartResumeWindow), ChunkSize: int32(chunkSize)}
	var rows []domain.UploadSession
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		rows, err = store.ActiveSurfaceUploads(ctx, filter)
		return err
	})
	if err != nil {
		return nil, err
	}
	result := make([]domain.UploadSession, len(rows))
	copy(result, rows)
	return result, nil
}
