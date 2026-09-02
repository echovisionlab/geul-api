package filemedia

import (
	"context"
	"fmt"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type multipartResumeSelection struct {
	activeSessions []model.UploadSession
	session        *model.UploadSession
	response       *managev1.InitiateMultipartUploadResponse
}

func (s *FileService) selectMultipartResumeCandidate(
	ctx context.Context,
	_ managev1.TranscodeEntityType,
	uploadType managev1.UploadType,
	entityID string,
	storedEntityType *string,
	identity fileIngestProjectionIdentity,
	fileName string,
	fileSize int64,
	mimeType string,
	fileLastModified *int64,
) (multipartResumeSelection, error) {
	return s.selectUnserializedMultipartResumeCandidate(
		ctx, uploadType, entityID, storedEntityType, identity,
		fileName, fileSize, mimeType, fileLastModified,
	)
}

func (s *FileService) selectUnserializedMultipartResumeCandidate(
	ctx context.Context,
	uploadType managev1.UploadType,
	entityID string,
	entityType *string,
	identity fileIngestProjectionIdentity,
	fileName string,
	fileSize int64,
	mimeType string,
	fileLastModified *int64,
) (multipartResumeSelection, error) {
	activeSessions, err := s.findActiveUploadSessionsForSurface(
		ctx, uploadType, entityID, entityType, identity,
	)
	if err != nil {
		return multipartResumeSelection{}, errs.Internal(
			fmt.Errorf("failed to find active upload sessions: %w", err),
		)
	}
	selection := multipartResumeSelection{activeSessions: activeSessions}
	for i := range activeSessions {
		activeSession := activeSessions[i]
		if !uploadSessionMatchesSelection(activeSession, fileName, fileSize, mimeType, fileLastModified) {
			continue
		}
		response, err := s.buildMultipartResumeResponse(ctx, activeSession)
		if err != nil {
			return multipartResumeSelection{}, err
		}
		selection.session = &activeSessions[i]
		selection.response = response
		break
	}
	return selection, nil
}

func (s *FileService) buildMultipartResumeResponse(
	ctx context.Context,
	session model.UploadSession,
) (*managev1.InitiateMultipartUploadResponse, error) {
	uploadedParts, err := s.loadUploadPartInfos(ctx, session.UploadID)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to load uploaded parts: %w", err))
	}
	s.refreshUploadSessionActivity(ctx, session.UploadID)
	response, err := multipartInitiateResponseFromSession(session, uploadedParts, true)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return response, nil
}
