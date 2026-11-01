package filemedia

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *Service) findActiveUploadSessionsForSurface(ctx context.Context, uploadType managev1.UploadType, entityID string, entityType *string, target fileIngestProjectionIdentity) ([]model.UploadSession, error) {
	if uploadType == managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE || isEditorFileIngestUploadType(uploadType) || target.mode == fileIngestTargetModeUnknown || (target.mode == fileIngestTargetModeGeneral && target.slotID == "") {
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
	result := make([]model.UploadSession, len(rows))
	for i, r := range rows {
		result[i] = uploadSessionFromStore(r)
	}
	return result, nil
}
