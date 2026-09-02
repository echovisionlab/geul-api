package filemedia

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type multipartCandidateSearch struct {
	request            *managev1.FindMultipartUploadCandidateRequest
	uploadType         managev1.UploadType
	entityType         string
	entityID           string
	fileName           string
	projectionIdentity fileIngestProjectionIdentity
}

func (s *FileService) prepareMultipartCandidateSearch(
	ctx context.Context,
	request *managev1.FindMultipartUploadCandidateRequest,
	memberID string,
) (multipartCandidateSearch, error) {
	search := multipartCandidateSearch{request: request, uploadType: request.UploadType}
	if s.getUploadConfig(search.uploadType) == nil {
		return search, errs.InvalidArgument(
			"upload_type",
			fmt.Sprintf("invalid upload type: %s", search.uploadType.String()),
		)
	}
	if request.EntityType != nil {
		search.entityType = request.EntityType.String()
	}
	search.entityID = strings.TrimSpace(request.EntityId)
	if err := validateIndependentFileCandidateIdentity(request, search.entityID); err != nil {
		return search, err
	}
	slotID := strings.TrimSpace(request.GetSlotId())
	resolvedSiteEntityID, err := resolveSiteUploadEntityID(
		s.legalRouteIdentity,
		search.uploadType,
		slotID,
	)
	if err != nil {
		return search, err
	}
	if resolvedSiteEntityID != "" {
		search.entityID = resolvedSiteEntityID
	}
	search.projectionIdentity, err = normalizeFileIngestProjectionIdentity(
		search.uploadType,
		request.GetEntityType(),
		slotID,
		request.ExpectedCurrentFileId,
	)
	if err != nil {
		return search, err
	}
	if err := s.checkEntityPermission(
		ctx, search.uploadType, search.entityType, search.entityID, memberID,
	); err != nil {
		return search, err
	}
	search.fileName, err = normalizeOptionalCandidateFilename(request.GetFileName())
	return search, err
}

func validateIndependentFileCandidateIdentity(
	request *managev1.FindMultipartUploadCandidateRequest,
	entityID string,
) error {
	if request.UploadType != managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE &&
		!isEditorFileIngestUploadType(request.UploadType) {
		return nil
	}
	if entityID != "" {
		return errs.InvalidArgument(
			"entity_id",
			"independent File upload must omit entity ID",
		)
	}
	if strings.TrimSpace(request.GetFileId()) == "" {
		return errs.Required("file_id")
	}
	if strings.TrimSpace(request.GetUploadId()) == "" {
		return errs.Required("upload_id")
	}
	return nil
}

func normalizeOptionalCandidateFilename(fileName string) (string, error) {
	if fileName == "" {
		return "", nil
	}
	normalized, err := normalizeNewDownloadFilename(fileName)
	if err != nil {
		return "", errs.InvalidArgument("file_name", err.Error())
	}
	return normalized, nil
}

func (s *FileService) findMultipartUploadCandidate(
	ctx context.Context,
	search multipartCandidateSearch,
) (model.UploadSession, bool, error) {
	var session model.UploadSession
	found, err := queryMultipartUploadCandidate(ctx, s.db, search, false, &session)
	return session, found, err
}

func queryMultipartUploadCandidate(
	ctx context.Context,
	db *gorm.DB,
	search multipartCandidateSearch,
	lockRows bool,
	result *model.UploadSession,
) (bool, error) {
	query := db.WithContext(ctx).
		Model(&model.UploadSession{}).
		Where(
			"upload_type = ? AND (status = ? OR (status IN ? AND last_activity_at >= ? AND chunk_size = ?))",
			search.uploadType.String(),
			model.UploadSessionStatusFinalizing,
			uploadPartWritableSessionStatuses(),
			time.Now().Add(-multipartResumeWindow),
			int32(chunkSize),
		)
	query = applyMultipartCandidateEntityFilters(query, search)
	var err error
	query, err = applyMultipartCandidateProjectionFilters(query, search)
	if err != nil {
		return false, err
	}
	query = applyMultipartCandidateRequestFilters(query, search)
	if lockRows {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err = query.
		Order("CASE WHEN status = 'finalizing' THEN 0 ELSE 1 END").
		Order("last_activity_at DESC").
		First(result).Error
	if err == gorm.ErrRecordNotFound {
		return false, nil
	}
	return err == nil, err
}

func applyMultipartCandidateEntityFilters(
	query *gorm.DB,
	search multipartCandidateSearch,
) *gorm.DB {
	if search.uploadType == managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE ||
		isEditorFileIngestUploadType(search.uploadType) {
		query = query.Where("entity_id IS NULL")
	} else {
		query = query.Where("entity_id = ?", search.entityID)
	}
	if search.request.EntityType != nil {
		query = query.Where("entity_type = ?", search.request.EntityType.String())
	}
	return query
}

func applyMultipartCandidateProjectionFilters(
	query *gorm.DB,
	search multipartCandidateSearch,
) (*gorm.DB, error) {
	identity := search.projectionIdentity
	switch identity.mode {
	case fileIngestTargetModeEditorFile:
		query = query.Where("slot_id IS NULL")
	case fileIngestTargetModeTrackProjection:
		query = query.Where("slot_id IS NULL")
	case fileIngestTargetModeGeneral:
		query = applyGeneralMultipartCandidateSlot(query, search.uploadType, identity.slotID)
	case fileIngestTargetModeUnknown:
		return nil, errs.Internal(fmt.Errorf("upload target classification is missing"))
	}
	if identity.expectedCurrentFileID == nil {
		return query.Where("expected_current_file_id IS NULL"), nil
	}
	return query.Where("expected_current_file_id = ?", *identity.expectedCurrentFileID), nil
}

func applyGeneralMultipartCandidateSlot(
	query *gorm.DB,
	uploadType managev1.UploadType,
	slotID string,
) *gorm.DB {
	if uploadType == managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE {
		return query.Where("slot_id IS NULL")
	}
	if slotID != "" {
		return query.Where("slot_id = ?", slotID)
	}
	return query
}

func applyMultipartCandidateRequestFilters(
	query *gorm.DB,
	search multipartCandidateSearch,
) *gorm.DB {
	request := search.request
	if fileID := request.GetFileId(); fileID != "" {
		query = query.Where("file_id = ?", fileID)
	}
	if uploadID := request.GetUploadId(); uploadID != "" {
		query = query.Where("upload_id = ?", uploadID)
	}
	if search.fileName != "" {
		query = query.Where("file_name = ?", search.fileName)
	}
	if fileSize := request.GetFileSize(); fileSize > 0 {
		query = query.Where("file_size = ?", fileSize)
	}
	if mimeType := request.GetMimeType(); mimeType != "" {
		query = query.Where("requested_mime = ?", mimeType)
	}
	if request.FileLastModified != nil {
		query = query.Where("file_last_modified = ?", request.GetFileLastModified())
	}
	return query
}

func multipartCandidateResponse(session model.UploadSession) *managev1.FindMultipartUploadCandidateResponse {
	extension := mediaExtension(&session.RequestedMime)
	return &managev1.FindMultipartUploadCandidateResponse{
		UploadId:              &session.UploadID,
		FileId:                &session.FileID,
		Extension:             &extension,
		TotalParts:            session.TotalParts,
		ChunkSize:             session.ChunkSize,
		Status:                uploadSessionStatusToProto(session.Status),
		FileName:              &session.FileName,
		FileSize:              session.FileSize,
		MimeType:              &session.RequestedMime,
		FileLastModified:      session.FileLastModified,
		LastActivityAt:        timestamppb.New(session.LastActivityAt),
		SlotId:                session.SlotID,
		IngestAttemptId:       session.AttemptID,
		ExpectedCurrentFileId: session.ExpectedFileID,
	}
}

func (s *FileService) prepareMultipartCandidateResponse(
	ctx context.Context,
	session model.UploadSession,
) (*managev1.FindMultipartUploadCandidateResponse, error) {
	response := multipartCandidateResponse(session)
	if session.Status == model.UploadSessionStatusFinalizing {
		var parts []model.UploadPart
		if err := s.db.WithContext(ctx).
			Where("upload_id = ?", session.UploadID).
			Order("part_number ASC").
			Find(&parts).Error; err != nil {
			return nil, err
		}
		response.UploadedParts = uploadPartInfos(parts)
		return response, nil
	}
	reconciliation, err := s.reconcileWritableMultipartResumeParts(ctx, session)
	if err != nil {
		return nil, err
	}
	response.Status = uploadSessionStatusToProto(reconciliation.session.Status)
	response.UploadedParts = reconciliation.parts
	return response, nil
}
