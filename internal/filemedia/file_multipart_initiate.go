package filemedia

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type multipartUploadInitiation struct {
	uploadType         managev1.UploadType
	entityType         string
	entityTypePtr      *string
	entityID           string
	slotID             string
	projectionIdentity fileIngestProjectionIdentity
	canonicalMime      string
	fileName           string
}

// InitiateMultipartUploadCommand starts the multipart upload of a File for
// an upload type and its target entity.
type InitiateMultipartUploadCommand struct {
	UploadType            managev1.UploadType
	EntityID              string
	EntityType            *managev1.TranscodeEntityType
	FileName              string
	FileSize              int64
	MimeType              string
	FileLastModified      *int64
	SlotID                *string
	ExpectedCurrentFileID *string
}

func (c InitiateMultipartUploadCommand) entityType() managev1.TranscodeEntityType {
	if c.EntityType == nil {
		return managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED
	}
	return *c.EntityType
}

// InitiateMultipartUpload starts or resumes a multipart upload.
func (s *Service) InitiateMultipartUpload(
	ctx context.Context,
	command InitiateMultipartUploadCommand,
) (_ *managev1.InitiateMultipartUploadResponse, resultErr error) {
	defer failurecause.AnnotatePrivate(&resultErr, command.FileName)

	initiation, err := s.prepareMultipartUploadInitiation(ctx, command)
	if err != nil {
		return nil, err
	}
	response, handled, err := s.resumeOrReplaceMultipartUpload(ctx, command, initiation)
	if err != nil || handled {
		return response, err
	}
	return s.createMultipartUploadSession(ctx, command, initiation)
}

func (s *Service) prepareMultipartUploadInitiation(
	ctx context.Context,
	request InitiateMultipartUploadCommand,
) (multipartUploadInitiation, error) {
	config := s.getUploadConfig(request.UploadType)
	if config == nil {
		message := fmt.Sprintf("invalid upload type: %s", request.UploadType.String())
		return multipartUploadInitiation{}, domainerr.InvalidField("upload_type", message)
	}
	user := auth.GetUser(ctx)
	if user == nil {
		return multipartUploadInitiation{}, domainerr.AuthenticationRequired()
	}

	initiation, err := normalizeMultipartUploadTarget(s.legalRouteIdentity, request)
	if err != nil {
		return multipartUploadInitiation{}, err
	}
	if err := s.checkEntityPermission(
		ctx,
		initiation.uploadType,
		initiation.entityType,
		initiation.entityID,
		user.MemberID.String(),
	); err != nil {
		return multipartUploadInitiation{}, err
	}

	canonicalMime, fileName, err := validateMultipartUploadFile(config, request, initiation.slotID)
	if err != nil {
		return multipartUploadInitiation{}, err
	}
	initiation.canonicalMime = canonicalMime
	initiation.fileName = fileName
	return initiation, nil
}

func normalizeMultipartUploadTarget(
	legalRoutes LegalRouteIdentity,
	request InitiateMultipartUploadCommand,
) (multipartUploadInitiation, error) {
	entityType := ""
	var entityTypePtr *string
	if request.EntityType != nil {
		entityType = request.EntityType.String()
		entityTypePtr = &entityType
	}
	initiation := multipartUploadInitiation{
		uploadType:    request.UploadType,
		entityType:    entityType,
		entityTypePtr: entityTypePtr,
		entityID:      strings.TrimSpace(request.EntityID),
		slotID:        strings.TrimSpace(derefString(request.SlotID)),
	}
	if initiation.uploadType == managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE && initiation.entityID != "" {
		return multipartUploadInitiation{}, domainerr.InvalidField("entity_id", "general File Manager upload must omit entity ID")
	}
	if isEditorFileIngestUploadType(initiation.uploadType) && initiation.entityID != "" {
		return multipartUploadInitiation{}, domainerr.InvalidField("entity_id", "editor File upload must omit document entity ID")
	}
	resolvedSiteEntityID, err := resolveSiteUploadEntityID(
		legalRoutes,
		initiation.uploadType,
		initiation.slotID,
	)
	if err != nil {
		return multipartUploadInitiation{}, err
	}
	if resolvedSiteEntityID != "" {
		initiation.entityID = resolvedSiteEntityID
	}
	initiation.projectionIdentity, err = normalizeFileIngestProjectionIdentity(
		initiation.uploadType,
		request.entityType(),
		initiation.slotID,
		request.ExpectedCurrentFileID,
	)
	return initiation, err
}

func validateMultipartUploadFile(
	config *domain.UploadConfig,
	request InitiateMultipartUploadCommand,
	slotID string,
) (string, string, error) {
	if request.FileSize > config.MaxSize {
		message := fmt.Sprintf("file size %d exceeds maximum %d", request.FileSize, config.MaxSize)
		return "", "", domainerr.InvalidField("file_size", message)
	}
	if config.MinSize > 0 && request.FileSize < config.MinSize {
		message := fmt.Sprintf("file size %d is below minimum %d", request.FileSize, config.MinSize)
		return "", "", domainerr.InvalidField("file_size", message)
	}

	allowed, canonicalMime := isMimeAllowed(request.MimeType, buildAllowedMimeSet(config.PermittedMimeTypes))
	if !allowed {
		message := unsupportedMimeMessage(request.MimeType, config.PermittedMimeTypes)
		return "", "", domainerr.InvalidField("mime_type", message)
	}
	if err := validateMultipartUploadTypeFile(request, slotID, canonicalMime); err != nil {
		return "", "", err
	}

	fileName, err := normalizeNewDownloadFilename(request.FileName)
	if err != nil {
		return "", "", domainerr.InvalidField("file_name", err.Error())
	}
	fileExtension := mediaExtension(&canonicalMime)
	if !strings.EqualFold(mediaauth.FilenameExtension(fileName), fileExtension) {
		message := fmt.Sprintf("filename extension must be .%s", fileExtension)
		return "", "", domainerr.InvalidField("file_name", message)
	}
	fileName = storedFileBasename(fileName, "download", fileExtension) + "." + fileExtension
	return canonicalMime, fileName, nil
}

func validateMultipartUploadTypeFile(
	request InitiateMultipartUploadCommand,
	slotID string,
	canonicalMime string,
) error {
	if request.UploadType == managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE {
		maxSize := generalFileMaxSize(canonicalMime)
		if request.FileSize > maxSize {
			message := fmt.Sprintf(
				"file size %d exceeds maximum %d for %s",
				request.FileSize,
				maxSize,
				canonicalMime,
			)
			return domainerr.InvalidField("file_size", message)
		}
	}
	if request.UploadType == managev1.UploadType_UPLOAD_TYPE_USER_AVATAR && canonicalMime != "image/webp" {
		return domainerr.InvalidField("mime_type", "direct avatar uploads must be WebP")
	}
	return validateSiteEmailLogoMime(request.UploadType, slotID, canonicalMime)
}

func (s *Service) resumeOrReplaceMultipartUpload(
	ctx context.Context,
	request InitiateMultipartUploadCommand,
	initiation multipartUploadInitiation,
) (*managev1.InitiateMultipartUploadResponse, bool, error) {
	selection, err := s.selectMultipartResumeCandidate(
		ctx,
		request.entityType(),
		initiation.uploadType,
		initiation.entityID,
		initiation.entityTypePtr,
		initiation.projectionIdentity,
		initiation.fileName,
		request.FileSize,
		initiation.canonicalMime,
		request.FileLastModified,
	)
	if err != nil {
		return nil, false, err
	}
	if selection.session != nil {
		response, err := s.prepareMultipartResumeResponse(ctx, *selection.session, selection.response)
		return response, true, err
	}
	if containsFinalizingUploadSession(selection.activeSessions) {
		return nil, false, domainerr.Precondition("an existing upload is finalizing; retry its completion before replacing it")
	}
	if err := s.cleanupSupersededUploadSessions(ctx, selection.activeSessions, "Upload replaced"); err != nil {
		if errors.Is(err, mediaasset.ErrUploadSessionNotAbortable) {
			return nil, false, domainerr.Precondition("an existing upload started finalizing before it could be replaced")
		}
		return nil, false, fmt.Errorf("failed to cleanup superseded upload sessions: %w", err)
	}
	return nil, false, nil
}

func (s *Service) createMultipartUploadSession(
	ctx context.Context,
	request InitiateMultipartUploadCommand,
	initiation multipartUploadInitiation,
) (*managev1.InitiateMultipartUploadResponse, error) {
	fileID := uuid.NewString()
	fileKey, err := s.buildManagedFileKey(fileID, initiation.canonicalMime)
	if err != nil {
		return nil, fmt.Errorf("failed to build file key: %w", err)
	}
	output, err := s.s3Client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:      aws.String(s.s3Bucket),
		Key:         aws.String(fileKey),
		ContentType: aws.String(initiation.canonicalMime),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create multipart upload: %w", err)
	}

	session := newMultipartUploadSession(request, initiation, fileID, *output.UploadId)
	s.beforeMultipartSessionInsert(session)
	if err := s.createUploadSession(ctx, &session); err != nil {
		s.abortMultipartUploadAfterInsertFailure(ctx, fileKey, output.UploadId)
		return nil, fmt.Errorf("failed to create upload session: %w", err)
	}
	s.afterMultipartSessionInsert(session)
	if err := s.publishMultipartUploadStarted(ctx, session); err != nil {
		return nil, err
	}
	return multipartInitiateResponseFromSession(session, nil, false), nil
}

func newMultipartUploadSession(
	request InitiateMultipartUploadCommand,
	initiation multipartUploadInitiation,
	fileID string,
	uploadID string,
) domain.UploadSession {
	now := time.Now()
	attemptID := uuid.NewString()
	totalParts := max(int32((request.FileSize+int64(chunkSize)-1)/int64(chunkSize)), 1)
	return domain.UploadSession{
		UploadID:         uploadID,
		FileID:           fileID,
		UploadType:       initiation.uploadType.String(),
		EntityID:         initiation.entityID,
		EntityType:       initiation.entityTypePtr,
		FileName:         initiation.fileName,
		FileSize:         request.FileSize,
		FileLastModified: request.FileLastModified,
		SlotID:           optionalNonEmptyString(initiation.projectionIdentity.slotID),
		AttemptID:        &attemptID,
		ExpectedFileID:   initiation.projectionIdentity.expectedCurrentFileID,
		RequestedMime:    initiation.canonicalMime,
		TotalParts:       totalParts,
		ChunkSize:        int32(chunkSize),
		Status:           domain.UploadSessionStatusInitiated,
		LastActivityAt:   now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

func (s *Service) beforeMultipartSessionInsert(session domain.UploadSession) {
	if session.UploadType == managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String() && s.testBeforeTrackSessionInsert != nil {
		s.testBeforeTrackSessionInsert(session)
	}
}

func (s *Service) afterMultipartSessionInsert(session domain.UploadSession) {
	if session.UploadType == managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String() && s.testAfterTrackSessionInsert != nil {
		s.testAfterTrackSessionInsert(session)
	}
}

func (s *Service) abortMultipartUploadAfterInsertFailure(
	ctx context.Context,
	fileKey string,
	uploadID *string,
) {
	cleanupCtx, cancel := newStorageCompensationContext(ctx)
	defer cancel()
	_, err := s.s3Client.AbortMultipartUpload(cleanupCtx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(s.s3Bucket), Key: aws.String(fileKey), UploadId: uploadID,
	})
	if err != nil {
		slog.ErrorContext(ctx, "Failed to abort multipart upload after session insert failure", failurecause.Attr(err), "fileKey", fileKey)
	}
}

func (s *Service) publishMultipartUploadStarted(ctx context.Context, session domain.UploadSession) error {
	emitter := newFileIngestEventEmitter(
		ctx,
		s.asyncPublisher,
		managev1.FileIngestSource_FILE_INGEST_SOURCE_DIRECT_UPLOAD,
		uploadSessionEntityTypeToEnum(session.EntityType),
		session.EntityID,
		"",
		session.FileID,
		session.FileSize,
	)
	if emitter == nil {
		return nil
	}
	if err := s.bindUploadSessionIngestEmitter(emitter, session); err != nil {
		return err
	}
	emitter.publishUploading(0, nil)
	return nil
}
