package filemedia

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/postgreslock"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

var remoteImportFileNamespace = uuid.MustParse("1b9e8e7d-3051-52c6-a782-3cb87f5b82cc")

const maxConcurrentRemoteImportAdvisoryLocks = 2

var remoteImportAdvisoryLockSlots = make(chan struct{}, maxConcurrentRemoteImportAdvisoryLocks)

type remoteImportOperationIdentity struct {
	fileID    string
	attemptID string
	durable   bool
}

func resolveRemoteImportOperationIdentity(
	opts remoteFileImportOptions,
	entityID string,
	slotID string,
	projection fileIngestProjectionIdentity,
) (remoteImportOperationIdentity, error) {
	_, dedicatedPublicAsset := dedicatedPublicAssetKind(opts.uploadType, slotID)
	if !isEditorFileIngestUploadType(opts.uploadType) &&
		!requiresDurableFileIngestAttachment(projection) &&
		!dedicatedPublicAsset {
		return remoteImportOperationIdentity{
			fileID:    uuid.NewString(),
			attemptID: uuid.NewString(),
		}, nil
	}

	correlationID := strings.TrimSpace(opts.correlationID)
	parsedCorrelationID, err := uuid.Parse(correlationID)
	if err != nil || correlationID == "" {
		return remoteImportOperationIdentity{}, fmt.Errorf("correlation_id must be a valid non-empty UUID for durable remote file ingest")
	}
	correlationID = parsedCorrelationID.String()

	target := struct {
		Version               int    `json:"version"`
		CorrelationID         string `json:"correlation_id"`
		UploadType            string `json:"upload_type"`
		EntityType            string `json:"entity_type"`
		EntityID              string `json:"entity_id"`
		SlotID                string `json:"slot_id"`
		ExpectedCurrentFileID string `json:"expected_current_file_id"`
	}{
		Version:               1,
		CorrelationID:         correlationID,
		UploadType:            opts.uploadType.String(),
		EntityType:            opts.transcodeEntityType.String(),
		EntityID:              strings.TrimSpace(entityID),
		SlotID:                strings.TrimSpace(slotID),
		ExpectedCurrentFileID: strings.TrimSpace(stringValueOrEmpty(projection.expectedCurrentFileID)),
	}
	encodedTarget, err := json.Marshal(target)
	if err != nil {
		return remoteImportOperationIdentity{}, fmt.Errorf("encode durable remote import identity: %w", err)
	}

	return remoteImportOperationIdentity{
		fileID:    uuid.NewSHA1(remoteImportFileNamespace, encodedTarget).String(),
		attemptID: correlationID,
		durable:   true,
	}, nil
}

func stringValueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func remoteImportAdvisoryLockKey(fileID string) int64 {
	digest := sha256.Sum256([]byte(strings.TrimSpace(fileID)))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}

func withRemoteImportAdvisoryLock(
	ctx context.Context,
	db *gorm.DB,
	fileID string,
	operation func(*gorm.DB) error,
) error {
	return postgreslock.WithBoundedAdvisoryLock(
		ctx,
		db,
		remoteImportAdvisoryLockKey(fileID),
		remoteImportAdvisoryLockSlots,
		"remote import",
		operation,
	)
}

func (s *FileService) restoreCompletedRemoteImport(
	ctx context.Context,
	opts remoteFileImportOptions,
	entityID string,
	slotID string,
	identity remoteImportOperationIdentity,
	projection fileIngestProjectionIdentity,
) (*remoteFileImportResult, bool, error) {
	file, found, err := s.loadCompletedRemoteImportFile(ctx, identity.fileID)
	if err != nil || !found {
		return nil, found, err
	}
	if err := validateCompletedRemoteImportFile(file, slotID, identity); err != nil {
		return nil, true, err
	}
	if err := s.validateCompletedRemoteImportBinding(ctx, file.ID, entityID, opts); err != nil {
		return nil, true, err
	}
	if err := s.verifyCompletedRemoteImportObject(ctx, file, opts.uploadType); err != nil {
		return nil, true, err
	}
	result, err := s.buildCompletedRemoteImportResult(file, slotID, identity, projection)
	return result, true, err
}

func (s *FileService) loadCompletedRemoteImportFile(
	ctx context.Context,
	fileID string,
) (model.File, bool, error) {
	var file model.File
	if err := s.db.WithContext(ctx).Where("id = ?", fileID).Take(&file).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return model.File{}, false, nil
		}
		return model.File{}, false, fmt.Errorf("load existing remote import file: %w", err)
	}
	return file, true, nil
}

func validateCompletedRemoteImportFile(
	file model.File,
	slotID string,
	identity remoteImportOperationIdentity,
) error {
	if file.DeleteRequestedAt != nil {
		return fmt.Errorf("existing remote import file is pending deletion")
	}
	expectedSlotID := optionalNonEmptyString(strings.TrimSpace(slotID))
	expectedAttemptID := optionalNonEmptyString(identity.attemptID)
	if !sameOptionalString(file.IngestSlotID, expectedSlotID) ||
		!sameOptionalString(file.IngestAttemptID, expectedAttemptID) {
		return fmt.Errorf("existing remote import file identity does not match the requested operation")
	}
	if file.FileSize <= 0 || len(file.SHA256) != sha256.Size ||
		strings.TrimSpace(file.MimeType) == "" ||
		file.Extension != mediaExtension(&file.MimeType) ||
		strings.TrimSpace(file.FileName) == "" ||
		file.FileName != storedFileBasename(file.FileName, file.ID, file.Extension) {
		return fmt.Errorf("existing remote import file metadata is incomplete")
	}
	return nil
}

func (s *FileService) validateCompletedRemoteImportBinding(
	ctx context.Context,
	fileID string,
	entityID string,
	opts remoteFileImportOptions,
) error {
	var binding model.FileIngestBinding
	if err := s.db.WithContext(ctx).Where("file_id = ?", fileID).Take(&binding).Error; err != nil {
		return fmt.Errorf("load existing remote import binding: %w", err)
	}
	var expectedEntityType *string
	if opts.transcodeEntityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED {
		value := opts.transcodeEntityType.String()
		expectedEntityType = &value
	}
	if binding.UploadType != opts.uploadType.String() ||
		binding.EntityID != entityID ||
		!sameOptionalString(binding.EntityType, expectedEntityType) {
		return fmt.Errorf("existing remote import binding does not match the requested operation")
	}
	return nil
}

func (s *FileService) verifyCompletedRemoteImportObject(
	ctx context.Context,
	file model.File,
	uploadType managev1.UploadType,
) error {
	target, err := CanonicalMediaObjectTargetForFile(file)
	if err != nil {
		return fmt.Errorf("build existing remote import object target: %w", err)
	}
	if err := s.verifyCompletedObjectContent(
		ctx,
		target.ObjectKey,
		file.FileSize,
		file.MimeType,
		uploadType,
	); err != nil {
		return fmt.Errorf("verify existing remote import object: %w", err)
	}
	return nil
}

func (s *FileService) buildCompletedRemoteImportResult(
	file model.File,
	slotID string,
	identity remoteImportOperationIdentity,
	projection fileIngestProjectionIdentity,
) (*remoteFileImportResult, error) {
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
		return nil, fmt.Errorf("sign existing remote import inline delivery: %w", err)
	}
	downloadFileName := CanonicalDownloadFilename(&file.FileName, file.ID, file.Extension)
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
		return nil, fmt.Errorf("sign existing remote import download delivery: %w", err)
	}
	return &remoteFileImportResult{
		fileID:                file.ID,
		fileName:              downloadFileName,
		mimeType:              file.MimeType,
		size:                  file.FileSize,
		inline:                inline,
		download:              download,
		slotID:                strings.TrimSpace(slotID),
		attemptID:             identity.attemptID,
		expectedCurrentFileID: projection.expectedCurrentFileID,
	}, nil
}

func (s *FileService) createOrRestoreVerifiedRemoteImportRecord(
	ctx context.Context,
	file structured.Fields,
	entityID string,
	slotID string,
	identity remoteImportOperationIdentity,
	projection fileIngestProjectionIdentity,
	opts remoteFileImportOptions,
	objectKey string,
) error {
	var authority verifiedFileIngestAuthority = trustedSystemFileIngestAuthority{}
	if opts.checkPermission {
		authority = requestFileIngestAuthority{}
	}
	createErr := s.createVerifiedFileIngestRecord(
		ctx,
		file,
		opts.uploadType,
		opts.transcodeEntityType,
		entityID,
		authority,
	)
	if createErr == nil {
		return nil
	}

	// A transaction error can mean that PostgreSQL committed the exact file and
	// binding but the acknowledgement was lost. Re-read the authoritative
	// domain rows and object before deciding whether the request-owned object is
	// safe to delete.
	_, found, restoreErr := s.restoreCompletedRemoteImport(
		ctx,
		opts,
		entityID,
		slotID,
		identity,
		projection,
	)
	if restoreErr != nil {
		return errors.Join(createErr, fmt.Errorf("validate remote import commit outcome: %w", restoreErr))
	}
	if found {
		return nil
	}

	s.deleteS3ObjectBestEffort(ctx, objectKey)
	return createErr
}

func (s *FileService) finishConfirmedRemoteImport(
	ctx context.Context,
	opts remoteFileImportOptions,
	result *remoteFileImportResult,
) error {
	if result == nil {
		return fmt.Errorf("confirmed remote import result is required")
	}
	if !opts.triggerTranscoding {
		return nil
	}

	return s.triggerFileScopedProcessingIfNeeded(ctx, result.fileID)
}
