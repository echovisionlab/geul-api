package filemedia

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *FileService) findActiveUploadSessionsForSurface(
	ctx context.Context,
	uploadType managev1.UploadType,
	entityID string,
	entityTypePtr *string,
	target fileIngestProjectionIdentity,
) ([]model.UploadSession, error) {
	return findActiveUploadSessionsForSurfaceWithDB(
		ctx,
		s.db,
		uploadType,
		entityID,
		entityTypePtr,
		target,
		false,
	)
}

func findActiveUploadSessionsForSurfaceWithDB(
	ctx context.Context,
	db *gorm.DB,
	uploadType managev1.UploadType,
	entityID string,
	entityTypePtr *string,
	target fileIngestProjectionIdentity,
	lockRows bool,
) ([]model.UploadSession, error) {
	query, supported := scopeUploadSessionsToAuthoritativeSurface(
		db.WithContext(ctx).Model(&model.UploadSession{}),
		uploadType,
		entityID,
		entityTypePtr,
		target,
	)
	if !supported {
		return nil, nil
	}
	query = query.
		Where("(status = ? OR (status IN ? AND last_activity_at >= ? AND chunk_size = ?))",
			model.UploadSessionStatusFinalizing,
			uploadPartWritableSessionStatuses(),
			time.Now().Add(-multipartResumeWindow),
			int32(chunkSize),
		)

	if target.expectedCurrentFileID == nil {
		query = query.Where("expected_current_file_id IS NULL")
	} else {
		query = query.Where("expected_current_file_id = ?", *target.expectedCurrentFileID)
	}
	if lockRows {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}

	var sessions []model.UploadSession
	if err := query.
		Order("CASE WHEN status = 'finalizing' THEN 0 ELSE 1 END").
		Order("last_activity_at DESC").
		Find(&sessions).Error; err != nil {
		return nil, err
	}
	return sessions, nil
}

func scopeUploadSessionsToAuthoritativeSurface(
	query *gorm.DB,
	uploadType managev1.UploadType,
	entityID string,
	entityTypePtr *string,
	target fileIngestProjectionIdentity,
) (*gorm.DB, bool) {
	if uploadType == managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE {
		// General uploads have no domain target and therefore do not participate
		// in implicit surface-wide resume selection.
		return query, false
	} else if isEditorFileIngestUploadType(uploadType) {
		// Independent editor File uploads may run concurrently. Resume requires
		// the explicit File/upload identity handled by FindMultipartUploadCandidate.
		return query, false
	} else {
		query = query.Where("upload_type = ? AND entity_id = ?", uploadType.String(), entityID)
	}
	if entityTypePtr != nil {
		query = query.Where("entity_type = ?", *entityTypePtr)
	}
	switch target.mode {
	case fileIngestTargetModeTrackProjection:
		query = query.Where("slot_id IS NULL")
	case fileIngestTargetModeEditorFile:
		query = query.Where("slot_id IS NULL")
	case fileIngestTargetModeGeneral:
		if target.slotID == "" {
			return query, false
		} else {
			query = query.Where("slot_id = ?", target.slotID)
		}
	case fileIngestTargetModeUnknown:
		return query, false
	}
	return query, true
}
