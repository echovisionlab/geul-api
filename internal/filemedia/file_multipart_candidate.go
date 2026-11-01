package filemedia

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

type multipartCandidateSearch struct {
	request            FindMultipartUploadCandidateQuery
	uploadType         UploadType
	entityType         string
	entityID           string
	fileName           string
	projectionIdentity fileIngestProjectionIdentity
}

func (s *Service) prepareMultipartCandidateSearch(
	ctx context.Context,
	request FindMultipartUploadCandidateQuery,
	memberID string,
) (multipartCandidateSearch, error) {
	search := multipartCandidateSearch{request: request, uploadType: request.UploadType}
	if s.getUploadConfig(search.uploadType) == nil {
		return search, domainerr.InvalidField("upload_type", fmt.Sprintf("invalid upload type: %s", search.uploadType.String()))
	}
	if request.EntityType != nil {
		search.entityType = request.EntityType.String()
	}
	search.entityID = strings.TrimSpace(request.EntityID)
	if err := validateIndependentFileCandidateIdentity(request, search.entityID); err != nil {
		return search, err
	}
	slotID := strings.TrimSpace(derefString(request.SlotID))
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
		request.entityType(),
		slotID,
		request.ExpectedCurrentFileID,
	)
	if err != nil {
		return search, err
	}
	if err := s.checkEntityPermission(
		ctx, search.uploadType, search.entityType, search.entityID, memberID,
	); err != nil {
		return search, err
	}
	search.fileName, err = normalizeOptionalCandidateFilename(derefString(request.FileName))
	return search, err
}

func validateIndependentFileCandidateIdentity(
	request FindMultipartUploadCandidateQuery,
	entityID string,
) error {
	if request.UploadType != UploadGeneralFile &&
		!isEditorFileIngestUploadType(request.UploadType) {
		return nil
	}
	if entityID != "" {
		return domainerr.InvalidField("entity_id", "independent File upload must omit entity ID")
	}
	if strings.TrimSpace(derefString(request.FileID)) == "" {
		return domainerr.Required("file_id")
	}
	if strings.TrimSpace(derefString(request.UploadID)) == "" {
		return domainerr.Required("upload_id")
	}
	return nil
}

func normalizeOptionalCandidateFilename(fileName string) (string, error) {
	if fileName == "" {
		return "", nil
	}
	normalized, err := normalizeNewDownloadFilename(fileName)
	if err != nil {
		return "", domainerr.InvalidField("file_name", err.Error())
	}
	return normalized, nil
}

func (s *Service) findMultipartUploadCandidate(
	ctx context.Context,
	search multipartCandidateSearch,
) (domain.UploadSession, bool, error) {
	if search.projectionIdentity.mode == fileIngestTargetModeUnknown {
		return domain.UploadSession{}, false, fmt.Errorf("upload target classification is missing")
	}
	var entityType *string
	if search.request.EntityType != nil {
		entityType = new(search.request.EntityType.String())
	}
	filter := domain.UploadSearch{UploadType: search.uploadType.String(), EntityID: search.entityID, EntityType: entityType, Mode: search.projectionIdentity.mode, SlotID: search.projectionIdentity.slotID, ExpectedCurrentFileID: search.projectionIdentity.expectedCurrentFileID, FileID: derefString(search.request.FileID), UploadID: derefString(search.request.UploadID), FileName: search.fileName, FileSize: search.request.FileSize, MimeType: derefString(search.request.MimeType), FileLastModified: search.request.FileLastModified, ActiveAfter: time.Now().Add(-multipartResumeWindow), ChunkSize: int32(chunkSize)}
	var row domain.UploadSession
	var found bool
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		row, found, err = store.FindUploadCandidate(ctx, filter)
		return err
	})
	return row, found, err
}

func multipartCandidateResponse(session domain.UploadSession) *MultipartCandidate {
	extension := mediaExtension(&session.RequestedMime)
	return &MultipartCandidate{
		ClientMediaBundleID:   session.ClientMediaBundleID,
		UploadID:              &session.UploadID,
		FileID:                &session.FileID,
		Extension:             &extension,
		TotalParts:            session.TotalParts,
		ChunkSize:             session.ChunkSize,
		Status:                session.Status,
		FileName:              &session.FileName,
		FileSize:              session.FileSize,
		MimeType:              &session.RequestedMime,
		FileLastModified:      session.FileLastModified,
		LastActivityAt:        &session.LastActivityAt,
		SlotID:                session.SlotID,
		IngestAttemptID:       session.AttemptID,
		ExpectedCurrentFileID: session.ExpectedFileID,
	}
}

func (s *Service) prepareMultipartCandidateResponse(
	ctx context.Context,
	session domain.UploadSession,
) (*MultipartCandidate, error) {
	response := multipartCandidateResponse(session)
	if session.Status == domain.UploadSessionStatusFinalizing {
		parts, err := s.loadUploadPartInfos(ctx, session.UploadID)
		if err != nil {
			return nil, err
		}
		response.UploadedParts = parts
		return response, nil
	}
	reconciliation, err := s.reconcileWritableMultipartResumeParts(ctx, session)
	if err != nil {
		return nil, err
	}
	response.Status = reconciliation.session.Status
	response.UploadedParts = reconciliation.parts
	return response, nil
}
