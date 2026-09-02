package filemedia

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type fileIngestTargetMode uint8

const (
	fileIngestTargetModeUnknown fileIngestTargetMode = iota
	fileIngestTargetModeGeneral
	fileIngestTargetModeEditorFile
	fileIngestTargetModeTrackProjection
)

type fileIngestProjectionIdentity struct {
	mode                  fileIngestTargetMode
	slotID                string
	expectedCurrentFileID *string
}

func (target fileIngestProjectionIdentity) requiresDurableAttachment() bool {
	return target.mode == fileIngestTargetModeTrackProjection
}

func isEditorFileIngestUploadType(uploadType managev1.UploadType) bool {
	switch uploadType {
	case managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH:
		return true
	default:
		return false
	}
}

func requiresDurableFileIngestAttachment(
	target fileIngestProjectionIdentity,
) bool {
	return target.requiresDurableAttachment()
}

func normalizeExpectedCurrentFileID(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil, errs.InvalidArgument(
			"expected_current_file_id",
			"expected current file ID must be omitted or non-empty",
		)
	}
	if _, err := uuid.Parse(normalized); err != nil {
		return nil, errs.InvalidArgument(
			"expected_current_file_id",
			"expected current file ID must be a UUID",
		)
	}
	return &normalized, nil
}

// normalizeFileIngestProjectionIdentity is the single semantic target policy
// for multipart, remote import, resume and projection finalization paths.
func normalizeFileIngestProjectionIdentity(
	uploadType managev1.UploadType,
	entityType managev1.TranscodeEntityType,
	slotID string,
	expectedCurrentFileID *string,
) (fileIngestProjectionIdentity, error) {
	normalizedSlotID := strings.TrimSpace(slotID)
	expected, err := normalizeExpectedCurrentFileID(expectedCurrentFileID)
	if err != nil {
		return fileIngestProjectionIdentity{}, err
	}
	if uploadType == managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE {
		return normalizeGeneralFileIngestProjectionIdentity(
			entityType, normalizedSlotID, expected,
		)
	}

	if isEditorFileIngestUploadType(uploadType) {
		return normalizeEditorFileIngestProjectionIdentity(
			uploadType, entityType, normalizedSlotID, expected,
		)
	}

	if uploadType == managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO {
		if entityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK {
			return fileIngestProjectionIdentity{}, errs.InvalidArgument(
				"entity_type",
				"track audio attachment requires a track entity",
			)
		}
		if normalizedSlotID != "" {
			return fileIngestProjectionIdentity{}, errs.InvalidArgument(
				"slot_id",
				"track audio attachment must omit slot ID",
			)
		}
		return fileIngestProjectionIdentity{
			mode:                  fileIngestTargetModeTrackProjection,
			expectedCurrentFileID: expected,
		}, nil
	}

	if expected != nil {
		return fileIngestProjectionIdentity{}, errs.InvalidArgument(
			"expected_current_file_id",
			"attachment CAS is supported only for Track original audio",
		)
	}
	return fileIngestProjectionIdentity{
		mode:   fileIngestTargetModeGeneral,
		slotID: normalizedSlotID,
	}, nil
}

func normalizeGeneralFileIngestProjectionIdentity(
	entityType managev1.TranscodeEntityType,
	slotID string,
	expectedCurrentFileID *string,
) (fileIngestProjectionIdentity, error) {
	if entityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED {
		return fileIngestProjectionIdentity{}, errs.InvalidArgument(
			"entity_type",
			"general File Manager upload must omit entity type",
		)
	}
	if slotID != "" || expectedCurrentFileID != nil {
		return fileIngestProjectionIdentity{}, errs.InvalidArgument(
			"slot_id",
			"general File Manager upload must omit entity, slot, and attachment CAS targets",
		)
	}
	return fileIngestProjectionIdentity{mode: fileIngestTargetModeGeneral}, nil
}

func normalizeEditorFileIngestProjectionIdentity(
	uploadType managev1.UploadType,
	entityType managev1.TranscodeEntityType,
	slotID string,
	expectedCurrentFileID *string,
) (fileIngestProjectionIdentity, error) {
	if entityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED {
		return fileIngestProjectionIdentity{}, errs.InvalidArgument(
			"entity_type",
			"editor File upload must omit document entity type",
		)
	}
	if slotID != "" || expectedCurrentFileID != nil {
		return fileIngestProjectionIdentity{}, errs.InvalidArgument(
			"slot_id",
			"editor file upload is independent of document block attachment",
		)
	}
	_ = uploadType
	return fileIngestProjectionIdentity{
		mode: fileIngestTargetModeEditorFile,
	}, nil
}

func fileIngestTargetFromStoredSession(
	session model.UploadSession,
) (fileIngestProjectionIdentity, error) {
	uploadTypeValue, ok := managev1.UploadType_value[session.UploadType]
	if !ok {
		return fileIngestProjectionIdentity{}, fmt.Errorf(
			"unsupported stored upload type %q",
			session.UploadType,
		)
	}
	target, err := normalizeFileIngestProjectionIdentity(
		managev1.UploadType(uploadTypeValue),
		uploadSessionEntityTypeToEnum(session.EntityType),
		derefString(session.SlotID),
		session.ExpectedFileID,
	)
	if err != nil {
		return fileIngestProjectionIdentity{}, err
	}
	return target, nil
}
