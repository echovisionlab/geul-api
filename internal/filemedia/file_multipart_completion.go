package filemedia

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	faviconpostgres "github.com/echovisionlab/geul-api/internal/favicon/postgres"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/postgreslock"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type multipartCompletion struct {
	clientMediaPlan *clientMediaPlan
	session         model.UploadSession
	uploadType      managev1.UploadType
	target          fileIngestProjectionIdentity
	objectKey       string
	verifiedMime    string
	correlationID   string
	uploadedParts   []model.UploadPart
	domainApplied   bool
	promotedAsset   *commonv1.AssetRef
	delivery        *commonv1.MediaDelivery
	progressEmitter *fileIngestEventEmitter
}

const maxConcurrentMultipartCompletionAdvisoryLocks = 2

var multipartCompletionAdvisoryLockSlots = make(
	chan struct{},
	maxConcurrentMultipartCompletionAdvisoryLocks,
)

func multipartCompletionAdvisoryLockKey(uploadID, fileID string) int64 {
	return remoteImportAdvisoryLockKey(
		"multipart-completion:" + strings.TrimSpace(uploadID) + "\x00" + strings.TrimSpace(fileID),
	)
}

func withMultipartCompletionAdvisoryLock(
	ctx context.Context,
	db *gorm.DB,
	uploadID string,
	fileID string,
	operation func(*gorm.DB) error,
) error {
	return postgreslock.WithBoundedAdvisoryLock(
		ctx,
		db,
		multipartCompletionAdvisoryLockKey(uploadID, fileID),
		multipartCompletionAdvisoryLockSlots,
		"multipart completion",
		operation,
	)
}

func (completion *multipartCompletion) publishFailure(err error) error {
	if completion == nil || completion.progressEmitter == nil {
		return err
	}
	progress := completion.progressEmitter.lastProgress
	if progress < 0 {
		progress = 100
	}
	completed := completion.session.FileSize
	completion.progressEmitter.publishFailed(clientErrorText(err), progress, &completed)
	return err
}

// CompleteMultipartUploadCommand completes the multipart upload of a File.
type CompleteMultipartUploadCommand struct {
	FileID              string
	UploadID            string
	CorrelationID       string
	ClientMediaBundleID string
}

// CompleteMultipartUpload coordinates the ordered multipart completion boundary.
func (s *Service) CompleteMultipartUpload(
	ctx context.Context,
	command CompleteMultipartUploadCommand,
) (*managev1.CompleteMultipartUploadResponse, error) {
	var response *managev1.CompleteMultipartUploadResponse
	err := withMultipartCompletionAdvisoryLock(
		ctx,
		s.db,
		command.UploadID,
		command.FileID,
		func(connection *gorm.DB) error {
			lockedService := *s
			lockedService.db = connection
			completion, found, err := lockedService.loadAuthorizedMultipartCompletion(ctx, command)
			if err != nil {
				return err
			}
			if !found {
				response, err = lockedService.recoverFinishedMultipartCompletion(ctx, command)
				return err
			}
			if err := lockedService.completeMultipartObject(ctx, completion); err != nil {
				return err
			}
			if err := lockedService.verifyMultipartCompletionObject(ctx, completion); err != nil {
				return err
			}
			if err := lockedService.persistMultipartCompletion(ctx, completion); err != nil {
				return err
			}
			if err := lockedService.registerClientMediaBundle(ctx, completion); err != nil {
				return completion.publishFailure(clientMediaCompletionError(err))
			}
			if err := lockedService.attachOrProjectMultipartCompletion(ctx, completion); err != nil {
				return err
			}
			if err := lockedService.publishMultipartDownstreamCommands(ctx, completion); err != nil {
				return err
			}
			response, err = lockedService.finishMultipartCompletion(ctx, completion)
			return err
		},
	)
	return response, err
}

func (s *Service) loadAuthorizedMultipartCompletion(
	ctx context.Context,
	request CompleteMultipartUploadCommand,
) (*multipartCompletion, bool, error) {
	var session model.UploadSession
	if err := s.db.WithContext(ctx).
		Where("upload_id = ? AND file_id = ?", request.UploadID, request.FileID).
		First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to load upload session: %w", err)
	}

	if err := clientMediaBundleMatches(session, request.ClientMediaBundleID); err != nil {
		return nil, false, err
	}
	objectKey, err := uploadSessionObjectKey(session)
	if err != nil {
		return nil, false, fmt.Errorf("invalid upload session target: %w", err)
	}
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, false, domainerr.AuthenticationRequired()
	}
	entityType := derefString(session.EntityType)
	uploadTypeValue, ok := managev1.UploadType_value[session.UploadType]
	if !ok {
		return nil, false, fmt.Errorf("upload session contains unsupported upload type %q", session.UploadType)
	}
	uploadType := managev1.UploadType(uploadTypeValue)
	if err := s.checkEntityPermission(ctx, uploadType, entityType, session.EntityID, user.MemberID.String()); err != nil {
		return nil, false, err
	}
	verifiedClientPlan, err := s.loadClientMediaPlan(ctx, session, user.MemberID.String())
	if err != nil {
		return nil, false, err
	}
	target, err := fileIngestTargetFromStoredSession(session)
	if err != nil {
		return nil, false, fmt.Errorf("invalid upload session ingest target: %w", err)
	}
	completion := &multipartCompletion{
		clientMediaPlan: verifiedClientPlan,
		session:         session,
		uploadType:      uploadType,
		target:          target,
		objectKey:       objectKey,
		correlationID:   request.CorrelationID,
	}
	completion.progressEmitter = newFileIngestEventEmitter(
		ctx,
		s.asyncPublisher,
		managev1.FileIngestSource_FILE_INGEST_SOURCE_DIRECT_UPLOAD,
		uploadSessionEntityTypeToEnum(session.EntityType),
		session.EntityID,
		request.CorrelationID,
		session.FileID,
		session.FileSize,
	)
	if completion.progressEmitter != nil {
		if err := s.bindUploadSessionIngestEmitter(completion.progressEmitter, session); err != nil {
			return nil, false, err
		}
	}

	verifiedMime, err := validateMultipartCompletionVerifiedMime(
		uploadType,
		session,
		s.getUploadConfig(uploadType),
	)
	if err != nil {
		return nil, false, completion.publishFailure(err)
	}
	if err := requireDirectClientMediaBundle(uploadType, verifiedMime, session, request.ClientMediaBundleID); err != nil {
		return nil, false, completion.publishFailure(err)
	}
	completion.verifiedMime = verifiedMime
	completion.session.FileName = mediaauth.CanonicalDownloadFilename(
		&completion.session.FileName,
		completion.session.FileID,
		mediaExtension(&verifiedMime),
	)

	now := time.Now()
	claimed, uploadedParts, err := s.claimMultipartCompletion(ctx, session, now)
	if err != nil {
		if mutation.IsClassified(err) {
			return nil, false, completion.publishFailure(err)
		}
		return nil, false, completion.publishFailure(
			fmt.Errorf("failed to update upload session state: %w", err),
		)
	}
	if !claimed {
		return nil, false, completion.publishFailure(
			domainerr.Precondition("upload session is no longer finalizable"),
		)
	}
	completion.session.Status = model.UploadSessionStatusFinalizing
	completion.uploadedParts = uploadedParts
	return completion, true, nil
}

func (s *Service) recoverFinishedMultipartCompletion(
	ctx context.Context,
	request CompleteMultipartUploadCommand,
) (*managev1.CompleteMultipartUploadResponse, error) {
	fileID := strings.TrimSpace(request.FileID)
	if !IsValidUUID(fileID) {
		return nil, domainerr.InvalidField("file_id", "invalid UUID format")
	}
	if strings.TrimSpace(request.UploadID) == "" {
		return nil, domainerr.Required("upload_id")
	}
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, domainerr.AuthenticationRequired()
	}

	file, uploadType, err := s.loadFinishedMultipartFile(ctx, fileID)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeManageFileDeliveries(ctx, []string{fileID}); err != nil {
		return nil, err
	}
	if file.ClientMediaBundleID != nil || request.ClientMediaBundleID != "" {
		if err := s.requireFinishedClientMediaBundle(ctx, file, request.ClientMediaBundleID, user); err != nil {
			return nil, err
		}
	}

	target, err := CanonicalMediaObjectTargetForFile(file)
	if err != nil {
		return nil, domainerr.Precondition("completed file object target is invalid").WithCause(failurecause.ServerFault(failurecause.WithAttrs(err, slog.String("file_id", file.ID))))
	}
	if err := s.verifyCompletedObjectContent(ctx, target.ObjectKey, file.FileSize, file.MimeType, uploadType); err != nil {
		return nil, fmt.Errorf("verify completed file object: %w", err)
	}

	promotedAsset, err := s.restoreFinishedMultipartPromotedAsset(
		ctx,
		uploadType,
		derefString(file.IngestSlotID),
		fileID,
	)
	if err != nil {
		return nil, err
	}
	promotedAsset, err = s.promoteFileScopedImageIfNeeded(
		ctx,
		uploadType,
		derefString(file.IngestSlotID),
		fileID,
		file.MimeType,
		promotedAsset,
	)
	if err != nil {
		return nil, err
	}
	if uploadType != managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO {
		if err := s.triggerFileScopedProcessingIfNeeded(ctx, fileID); err != nil {
			return nil, err
		}
	}
	delivery, err := s.signFinishedMultipartDelivery(file, promotedAsset)
	if err != nil {
		return nil, err
	}
	return &managev1.CompleteMultipartUploadResponse{
		FileId:   file.ID,
		Delivery: delivery,
	}, nil
}

// loadFinishedMultipartFile loads the deliverable File a completed upload
// left behind and the upload type its ingest binding recorded.
func (s *Service) loadFinishedMultipartFile(ctx context.Context, fileID string) (model.File, managev1.UploadType, error) {
	// A different upload ID must not bypass an active session and return before
	// attachment/downstream confirmation. Successful completion deletes the only
	// session for this file after every required side effect has succeeded.
	var active bool
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		active, err = store.HasFileUploadSessions(ctx, fileID)
		return err
	}); err != nil {
		return model.File{}, 0, fmt.Errorf("inspect completed upload authority: %w", err)
	}
	if active {
		return model.File{}, 0, domainerr.New(domainerr.NotFound, "", "upload session not found")
	}
	var stored File
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		stored, err = store.IngestFile(ctx, fileID)
		return err
	}); err != nil {
		if errors.Is(err, domain.ErrFileNotFound) {
			return model.File{}, 0, domainerr.New(domainerr.NotFound, "", "upload session not found")
		}
		return model.File{}, 0, fmt.Errorf("load completed file: %w", err)
	}
	file := model.File(stored)

	if file.DeleteRequestedAt != nil || file.FileSize <= 0 ||
		strings.TrimSpace(file.FileName) == "" || strings.TrimSpace(file.MimeType) == "" ||
		file.Extension != mediaExtension(&file.MimeType) ||
		file.FileName != storedFileBasename(file.FileName, file.ID, file.Extension) {
		return model.File{}, 0, domainerr.Precondition("completed file metadata is not deliverable")
	}

	var binding domain.IngestBinding
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		binding, err = store.IngestBinding(ctx, fileID)
		return err
	}); err != nil {
		if errors.Is(err, domain.ErrIngestBindingNotFound) {
			return model.File{}, 0, domainerr.New(domainerr.NotFound, "", "upload session not found")
		}
		return model.File{}, 0, fmt.Errorf("load completed file binding: %w", err)
	}

	uploadTypeValue, ok := managev1.UploadType_value[binding.UploadType]
	if !ok {
		return model.File{}, 0, domainerr.Precondition("completed file binding has unsupported upload type")
	}
	return file, managev1.UploadType(uploadTypeValue), nil
}

// requireFinishedClientMediaBundle requires the completed File's client media
// bundle to be the requested one, uploaded by the caller, with its
// derivatives ready.
func (s *Service) requireFinishedClientMediaBundle(ctx context.Context, file model.File, bundleID string, user *auth.UserInfo) error {
	if file.ClientMediaBundleID == nil || *file.ClientMediaBundleID != bundleID {
		return domainerr.Precondition("completed client media bundle does not match")
	}
	if file.UploadedByMemberID == nil || *file.UploadedByMemberID != user.MemberID.String() {
		return domainerr.Denied("completed client media bundle belongs to another member")
	}
	ready, err := s.clientMediaFileReady(ctx, File(file))
	if err != nil {
		return err
	}
	if !ready {
		return domainerr.Precondition("completed client media derivatives are not ready")
	}
	return nil
}

func (s *Service) signFinishedMultipartDelivery(file model.File, promotedAsset *commonv1.AssetRef) (*commonv1.MediaDelivery, error) {
	inline, err := buildExpiringMediaFileRef(
		s.mediaDomain,
		s.mediaSecret,
		file.ID,
		file.Extension,
		file.MimeType,
		nil,
		mediaauth.PurposeInline,
		mediaauth.InlineTTL,
	)
	if err != nil {
		return nil, fmt.Errorf("sign completed file inline delivery: %w", err)
	}
	downloadFileName := mediaauth.CanonicalDownloadFilename(&file.FileName, file.ID, file.Extension)
	download, err := buildExpiringMediaFileRef(
		s.mediaDomain,
		s.mediaSecret,
		file.ID,
		file.Extension,
		file.MimeType,
		&downloadFileName,
		mediaauth.PurposeDownload,
		s.effectiveDownloadTTL(),
	)
	if err != nil {
		return nil, fmt.Errorf("sign completed file download delivery: %w", err)
	}
	return &commonv1.MediaDelivery{
		FileId:    file.ID,
		Extension: file.Extension,
		MimeType:  file.MimeType,
		FileSize:  file.FileSize,
		FileName:  &downloadFileName,
		Asset:     promotedAsset,
		Inline:    inline,
		Download:  download,
	}, nil
}

func (s *Service) restoreFinishedMultipartPromotedAsset(
	ctx context.Context,
	uploadType managev1.UploadType,
	slotID string,
	fileID string,
) (*commonv1.AssetRef, error) {
	if uploadType == managev1.UploadType_UPLOAD_TYPE_SITE_FAVICON {
		set, err := faviconpostgres.LoadSet(ctx, s.db, s.cdnDomain, fileID)
		if err != nil {
			return nil, fmt.Errorf("restore completed favicon asset: %w", err)
		}
		if set == nil || set.IconPNG32 == nil {
			return nil, domainerr.Precondition("completed favicon asset is not ready")
		}
		return mediaasset.ReferenceProto(set.IconPNG32), nil
	}
	kind, promoted := dedicatedPublicAssetKind(uploadType, slotID)
	if !promoted {
		return nil, nil
	}
	asset, err := mediaasset.ReferenceResultProto(mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.db), s.cdnDomain).
		ReadyAssetRefForSourceFile(ctx, fileID, kind))
	if err != nil {
		return nil, err
	}
	return asset, nil
}

func (s *Service) completeMultipartObject(
	ctx context.Context,
	completion *multipartCompletion,
) error {
	slog.Debug("Completing multipart upload",
		"uploadId", completion.session.UploadID,
		"fileId", completion.session.FileID,
		"uploadType", completion.session.UploadType,
		"entityType", derefString(completion.session.EntityType),
		"entityId", completion.session.EntityID,
		"slotId", derefString(completion.session.SlotID),
		"attemptId", derefString(completion.session.AttemptID),
		"correlationId", completion.correlationID,
		"fileSize", completion.session.FileSize,
		"totalParts", completion.session.TotalParts,
		"uploadedPartCount", len(completion.uploadedParts),
		"uploadedPartNumbers", uploadPartModelNumbers(completion.uploadedParts),
	)
	domainApplied, err := s.completedFileRecordExists(ctx, completion.session.FileID)
	if err != nil {
		return fmt.Errorf("failed to inspect completed file record: %w", err)
	}
	completion.domainApplied = domainApplied
	if domainApplied {
		return nil
	}

	storedParts, err := s.listAndValidateMultipartResumeParts(
		ctx,
		completion.session,
		completion.objectKey,
	)
	if err != nil {
		if IsMissingMultipartUploadAbortError(err) {
			if verifyErr := s.verifyCompletedObjectMetadata(
				ctx,
				completion.objectKey,
				completion.session.FileSize,
				completion.verifiedMime,
			); verifyErr == nil {
				slog.WarnContext(ctx, "Recovered completed object after missing multipart upload", failurecause.Attr(err), "uploadId", completion.session.UploadID, "fileId", completion.session.FileID, "correlationId", completion.correlationID)
				return nil
			} else {
				err = errors.Join(err, verifyErr)
				terminal := isMissingStoredObjectError(verifyErr) ||
					errors.Is(verifyErr, errCompletedObjectMetadataMismatch)
				return s.handleMultipartCompletionStorageError(
					ctx,
					completion,
					err,
					terminal,
					errors.Is(verifyErr, errCompletedObjectMetadataMismatch),
				)
			}
		}
		return completion.publishFailure(
			fmt.Errorf("failed to reconcile multipart parts before completion: %w", err),
		)
	}
	if err := validateMultipartCompletionParts(completion.session.FileSize, multipartSessionChunkSize(completion.session), completion.session.TotalParts, storedParts); err != nil {
		return completion.publishFailure(err)
	}
	// S3 owns the actual bytes and ETags. A still-valid presigned URL can
	// replace a previously confirmed part, so the completion boundary must not
	// trust the upload_part cache captured before the finalizing transition.
	completion.uploadedParts = storedParts

	completeStartedAt := time.Now()
	_, err = s.s3Client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(s.s3Bucket),
		Key:      aws.String(completion.objectKey),
		UploadId: aws.String(completion.session.UploadID),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: toCompletedParts(completion.uploadedParts),
		},
	})
	terminalFailure := false
	deleteCompletedObject := false
	if err != nil && IsMissingMultipartUploadAbortError(err) {
		if verifyErr := s.verifyCompletedObjectMetadata(
			ctx,
			completion.objectKey,
			completion.session.FileSize,
			completion.verifiedMime,
		); verifyErr != nil {
			err = errors.Join(err, verifyErr)
			terminalFailure = isMissingStoredObjectError(verifyErr) ||
				errors.Is(verifyErr, errCompletedObjectMetadataMismatch)
			deleteCompletedObject = errors.Is(verifyErr, errCompletedObjectMetadataMismatch)
		} else {
			slog.WarnContext(ctx, "Recovered multipart completion after upload disappeared", failurecause.Attr(err), "uploadId", completion.session.UploadID, "fileId", completion.session.FileID, "correlationId", completion.correlationID)
			err = nil
		}
	}
	if err != nil {
		return s.handleMultipartCompletionStorageError(
			ctx, completion, err, terminalFailure, deleteCompletedObject,
		)
	}
	slog.Debug("Multipart upload completed in S3",
		"uploadId", completion.session.UploadID,
		"fileId", completion.session.FileID,
		"correlationId", completion.correlationID,
		"totalParts", completion.session.TotalParts,
		"uploadedPartNumbers", uploadPartModelNumbers(completion.uploadedParts),
		"durationMs", time.Since(completeStartedAt).Milliseconds(),
	)
	return nil
}

func (s *Service) handleMultipartCompletionStorageError(
	ctx context.Context,
	completion *multipartCompletion,
	completionErr error,
	terminal bool,
	deleteCompletedObject bool,
) error {
	// A timeout or transient object-store/provider failure is ambiguous: the
	// multipart upload may still exist, or Complete may have succeeded after
	// the response was lost. Keep finalizing as the retry authority. Only a
	// missing upload plus a definitively absent/mismatched completed object is
	// terminal.
	if !terminal {
		return completion.publishFailure(
			fmt.Errorf("failed to complete multipart upload: %w", completionErr),
		)
	}
	if deleteCompletedObject {
		if err := s.deleteMultipartCompletionObject(ctx, completion.objectKey); err != nil {
			completionErr = errors.Join(completionErr, fmt.Errorf("delete invalid completed object: %w", err))
			return completion.publishFailure(
				fmt.Errorf("failed to complete multipart upload: %w", completionErr),
			)
		}
	}
	s.markMultipartCompletionFailed(ctx, completion.session.UploadID)
	return completion.publishFailure(
		fmt.Errorf("failed to complete multipart upload: %w", completionErr),
	)
}

func (s *Service) verifyCompletedObjectContent(
	ctx context.Context,
	objectKey string,
	expectedSize int64,
	expectedMime string,
	uploadType managev1.UploadType,
) error {
	if err := s.verifyCompletedObjectMetadata(ctx, objectKey, expectedSize, expectedMime); err != nil {
		return err
	}
	prefix, err := s.readStoredObjectPrefix(ctx, objectKey, expectedSize)
	if err != nil {
		return err
	}
	config := s.getUploadConfig(uploadType)
	if config == nil {
		return fmt.Errorf("%w: upload type no longer has a MIME policy", errStoredObjectMIMEMismatch)
	}
	detectedMIME := detectCanonicalMime(prefix, buildAllowedMimeSet(config.PermittedMimeTypes))
	if detectedMIME != expectedMime {
		return fmt.Errorf(
			"%w: completed object MIME %q does not match verified MIME %q",
			errStoredObjectMIMEMismatch,
			detectedMIME,
			expectedMime,
		)
	}
	if detectedMIME == "model/gltf-binary" {
		if err := validateGLBUploadSize(prefix, expectedSize); err != nil {
			return fmt.Errorf("%w: %v", errStoredObjectMIMEMismatch, err)
		}
	}
	return nil
}

func (s *Service) verifyMultipartCompletionObject(
	ctx context.Context,
	completion *multipartCompletion,
) error {
	err := s.verifyCompletedObjectContent(
		ctx,
		completion.objectKey,
		completion.session.FileSize,
		completion.verifiedMime,
		completion.uploadType,
	)
	if err == nil {
		return nil
	}
	if completion.domainApplied {
		return fmt.Errorf("failed to verify previously completed upload: %w", err)
	}
	// Metadata and MIME mismatches are definitive invalid objects. Fetch/read
	// failures are ambiguous and must leave the session finalizing for retry.
	if errors.Is(err, errCompletedObjectMetadataMismatch) || errors.Is(err, errStoredObjectMIMEMismatch) {
		if deleteErr := s.deleteMultipartCompletionObject(ctx, completion.objectKey); deleteErr != nil {
			err = errors.Join(err, fmt.Errorf("delete invalid completed object: %w", deleteErr))
		} else {
			s.markMultipartCompletionFailed(ctx, completion.session.UploadID)
		}
	}
	return completion.publishFailure(
		fmt.Errorf("failed to verify completed upload: %w", err),
	)
}

func (s *Service) deleteMultipartCompletionObject(ctx context.Context, objectKey string) error {
	cleanupCtx, cancel := newStorageCompensationContext(ctx)
	defer cancel()
	_, err := s.s3Client.DeleteObject(cleanupCtx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.s3Bucket),
		Key:    aws.String(objectKey),
	})
	return err
}

func (s *Service) persistMultipartCompletion(
	ctx context.Context,
	completion *multipartCompletion,
) error {
	resolvedEntityType := uploadSessionEntityTypeToEnum(completion.session.EntityType)
	if completion.domainApplied {
		if err := s.validateCompletedFileRecord(
			ctx,
			completion.session,
			completion.uploadType,
			resolvedEntityType,
		); err != nil {
			return fmt.Errorf("failed to validate completed upload state: %w", err)
		}
		return nil
	}
	authority, err := newMultipartFileIngestAuthority(completion.session)
	if err != nil {
		return completion.publishFailure(err)
	}
	err = s.createVerifiedFileIngestRecord(
		ctx,
		completedMultipartFileRecord(completion.session),
		completion.uploadType,
		resolvedEntityType,
		completion.session.EntityID,
		authority,
	)
	if err != nil {
		// The object has already been completed and verified. Preserve it and the
		// finalizing session so a transient database/CAS failure can retry the same
		// file identity instead of destructively restarting the upload.
		return completion.publishFailure(
			fmt.Errorf("failed to create verified file record: %w", err),
		)
	}
	return nil
}

func (s *Service) attachOrProjectMultipartCompletion(
	ctx context.Context,
	completion *multipartCompletion,
) error {
	promotedAsset, err := s.promoteDedicatedPublicUploadAsset(
		ctx,
		completion.uploadType,
		derefString(completion.session.SlotID),
		completion.session.FileID,
	)
	if err != nil {
		// The source file/object is already verified and the session is the
		// completion retry authority. Promotion may have written a reusable
		// failed/ready asset before its response was lost, so never compensate by
		// deleting the source or by taking the session out of finalizing here.
		return completion.publishFailure(err)
	}
	promotedAsset, err = s.promoteFileScopedImageIfNeeded(
		ctx,
		completion.uploadType,
		derefString(completion.session.SlotID),
		completion.session.FileID,
		completion.verifiedMime,
		promotedAsset,
	)
	if err != nil {
		return completion.publishFailure(err)
	}
	completion.promotedAsset = promotedAsset
	if completion.progressEmitter != nil {
		completed := completion.session.FileSize
		completion.progressEmitter.publishFinalized(100, &completed)
	}

	extension := mediaExtension(&completion.verifiedMime)
	inline, err := buildExpiringMediaFileRef(
		s.mediaDomain,
		s.mediaSecret,
		completion.session.FileID,
		extension,
		completion.verifiedMime,
		nil,
		mediaauth.PurposeInline,
		mediaauth.InlineTTL,
	)
	if err != nil {
		return completion.publishFailure(err)
	}
	download, err := buildExpiringMediaFileRef(
		s.mediaDomain,
		s.mediaSecret,
		completion.session.FileID,
		extension,
		completion.verifiedMime,
		&completion.session.FileName,
		mediaauth.PurposeDownload,
		s.effectiveDownloadTTL(),
	)
	if err != nil {
		return completion.publishFailure(err)
	}
	completion.delivery = &commonv1.MediaDelivery{
		FileId:    completion.session.FileID,
		Extension: extension,
		MimeType:  completion.verifiedMime,
		FileSize:  completion.session.FileSize,
		FileName:  &completion.session.FileName,
		Asset:     completion.promotedAsset,
		Inline:    inline,
		Download:  download,
	}

	// Only Track original audio has a durable attachment finalizer. Document
	// Block attachment is a separate revision-CAS mutation.
	if !completion.target.requiresDurableAttachment() {
		return nil
	}
	if err := s.attachPreparedTrack(ctx, completion); err != nil {
		return completion.publishFailure(err)
	}
	if completion.progressEmitter == nil {
		return nil
	}
	if err := completion.progressEmitter.publishAttachedConfirmed(
		completion.session.FileName,
		completion.verifiedMime,
		completion.session.FileSize,
	); err != nil {
		return fmt.Errorf("failed to confirm durable file attachment: %w", err)
	}
	return nil
}

// Prepared media is already READY; attach through the existing Track CAS before
// publishing the existing durable projection and ready lifecycle events.
func (s *Service) attachPreparedTrack(ctx context.Context, completion *multipartCompletion) error {
	if completion.session.ClientMediaBundleID == nil {
		return nil
	}
	_, err := mutation.Run(ctx, s.storeMutations, mutation.StoreSpec[TrackOriginalAudioAttachment, Store]{
		Access: mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
			return requireFreshFileIngestAuthority(ctx, store, s, completion.uploadType, uploadSessionEntityTypeToEnum(completion.session.EntityType), completion.session.EntityID, lockActive)
		}),
		Change: func(ctx context.Context, store Store) (TrackOriginalAudioAttachment, error) {
			target, err := validateTrackOriginalAudioTarget(AttachTrackOriginalAudioCommand{TrackID: completion.session.EntityID, VerifiedFileID: completion.session.FileID, IngestAttemptID: derefString(completion.session.AttemptID), ExpectedCurrentFileID: completion.session.ExpectedFileID})
			if err != nil {
				return TrackOriginalAudioAttachment{}, err
			}
			return s.attachTrackOriginalAudio(ctx, store, target)
		},
		Audit: func(TrackOriginalAudioAttachment) mutation.Audit { return mutation.Unchanged() },
	})
	return err
}

func (s *Service) publishMultipartDownstreamCommands(
	ctx context.Context,
	completion *multipartCompletion,
) error {
	if completion.session.ClientMediaBundleID != nil {
		return s.publishClientMediaReady(ctx, completion)
	}
	if completion.target.requiresDurableAttachment() {
		return nil
	}
	if err := s.triggerFileScopedProcessingIfNeeded(ctx, completion.session.FileID); err != nil {
		return completion.publishFailure(err)
	}
	return nil
}

func (s *Service) finishMultipartCompletion(
	ctx context.Context,
	completion *multipartCompletion,
) (*managev1.CompleteMultipartUploadResponse, error) {
	if err := s.cleanupClientMediaStaging(ctx, completion.session); err != nil {
		return nil, err
	}
	if err := s.deleteCompletedUploadSession(ctx, completion.session.UploadID); err != nil {
		return nil, fmt.Errorf("failed to delete completed upload session: %w", err)
	}
	return &managev1.CompleteMultipartUploadResponse{
		FileId:   completion.session.FileID,
		Delivery: completion.delivery,
	}, nil
}

func (s *Service) markMultipartCompletionFailed(ctx context.Context, uploadID string) {
	now := time.Now()
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		return unit.Store().FailFinalizingUpload(ctx, uploadID, now)
	})
	if err != nil {
		slog.ErrorContext(ctx, "Failed to mark multipart completion failed", failurecause.Attr(err), "uploadId", uploadID)
	}
}
