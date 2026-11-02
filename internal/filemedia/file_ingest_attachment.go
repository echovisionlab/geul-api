package filemedia

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

type fileIngestTargetMode = domain.UploadTargetMode

const (
	fileIngestTargetModeUnknown         = domain.UploadTargetUnknown
	fileIngestTargetModeGeneral         = domain.UploadTargetGeneral
	fileIngestTargetModeEditorFile      = domain.UploadTargetEditorFile
	fileIngestTargetModeTrackProjection = domain.UploadTargetTrackProjection
)

type fileIngestProjectionIdentity struct {
	mode                  fileIngestTargetMode
	slotID                string
	expectedCurrentFileID *string
}

func (target fileIngestProjectionIdentity) requiresDurableAttachment() bool {
	return target.mode == fileIngestTargetModeTrackProjection
}

func isEditorFileIngestUploadType(uploadType UploadType) bool {
	switch uploadType {
	case UploadEditorImage,
		UploadEditorAudio,
		UploadEditorVideo,
		UploadEditorAttachment,
		UploadEditorMesh:
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
		return nil, domainerr.InvalidField("expected_current_file_id", "expected current file ID must be omitted or non-empty")
	}
	if _, err := uuid.Parse(normalized); err != nil {
		return nil, domainerr.InvalidField("expected_current_file_id", "expected current file ID must be a UUID")
	}
	return &normalized, nil
}

// normalizeFileIngestProjectionIdentity is the single semantic target policy
// for multipart, remote import, resume and projection finalization paths.
func normalizeFileIngestProjectionIdentity(
	uploadType UploadType,
	entityType EntityType,
	slotID string,
	expectedCurrentFileID *string,
) (fileIngestProjectionIdentity, error) {
	normalizedSlotID := strings.TrimSpace(slotID)
	expected, err := normalizeExpectedCurrentFileID(expectedCurrentFileID)
	if err != nil {
		return fileIngestProjectionIdentity{}, err
	}
	if uploadType == UploadGeneralFile {
		return normalizeGeneralFileIngestProjectionIdentity(
			entityType, normalizedSlotID, expected,
		)
	}

	if isEditorFileIngestUploadType(uploadType) {
		return normalizeEditorFileIngestProjectionIdentity(
			uploadType, entityType, normalizedSlotID, expected,
		)
	}

	if uploadType == UploadTrackAudio {
		if entityType != EntityTrack {
			return fileIngestProjectionIdentity{}, domainerr.InvalidField("entity_type", "track audio attachment requires a track entity")
		}
		if normalizedSlotID != "" {
			return fileIngestProjectionIdentity{}, domainerr.InvalidField("slot_id", "track audio attachment must omit slot ID")
		}
		return fileIngestProjectionIdentity{
			mode:                  fileIngestTargetModeTrackProjection,
			expectedCurrentFileID: expected,
		}, nil
	}

	if expected != nil {
		return fileIngestProjectionIdentity{}, domainerr.InvalidField("expected_current_file_id", "attachment CAS is supported only for Track original audio")
	}
	return fileIngestProjectionIdentity{
		mode:   fileIngestTargetModeGeneral,
		slotID: normalizedSlotID,
	}, nil
}

func normalizeGeneralFileIngestProjectionIdentity(
	entityType EntityType,
	slotID string,
	expectedCurrentFileID *string,
) (fileIngestProjectionIdentity, error) {
	if entityType != EntityUnspecified {
		return fileIngestProjectionIdentity{}, domainerr.InvalidField("entity_type", "general File Manager upload must omit entity type")
	}
	if slotID != "" || expectedCurrentFileID != nil {
		return fileIngestProjectionIdentity{}, domainerr.InvalidField("slot_id", "general File Manager upload must omit entity, slot, and attachment CAS targets")
	}
	return fileIngestProjectionIdentity{mode: fileIngestTargetModeGeneral}, nil
}

func normalizeEditorFileIngestProjectionIdentity(
	uploadType UploadType,
	entityType EntityType,
	slotID string,
	expectedCurrentFileID *string,
) (fileIngestProjectionIdentity, error) {
	if entityType != EntityUnspecified {
		return fileIngestProjectionIdentity{}, domainerr.InvalidField("entity_type", "editor File upload must omit document entity type")
	}
	if slotID != "" || expectedCurrentFileID != nil {
		return fileIngestProjectionIdentity{}, domainerr.InvalidField("slot_id", "editor file upload is independent of document block attachment")
	}
	_ = uploadType
	return fileIngestProjectionIdentity{
		mode: fileIngestTargetModeEditorFile,
	}, nil
}

func fileIngestTargetFromStoredSession(
	session domain.UploadSession,
) (fileIngestProjectionIdentity, error) {
	uploadTypeValue, ok := domain.ParseUploadType(session.UploadType)
	if !ok {
		return fileIngestProjectionIdentity{}, fmt.Errorf(
			"unsupported stored upload type %q",
			session.UploadType,
		)
	}
	target, err := normalizeFileIngestProjectionIdentity(
		UploadType(uploadTypeValue),
		uploadSessionEntityTypeToEnum(session.EntityType),
		derefString(session.SlotID),
		session.ExpectedFileID,
	)
	if err != nil {
		return fileIngestProjectionIdentity{}, err
	}
	return target, nil
}
