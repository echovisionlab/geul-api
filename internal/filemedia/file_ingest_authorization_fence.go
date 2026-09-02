package filemedia

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type verifiedFileIngestAuthority interface {
	revalidate(
		context.Context,
		*gorm.DB,
		*FileService,
		string,
		managev1.UploadType,
		managev1.TranscodeEntityType,
		string,
	) error
}

// trustedSystemFileIngestAuthority is reserved for authenticated internal
// worker/system ingress that has no request actor. Request paths must use an
// actor-bearing authority fence.
type trustedSystemFileIngestAuthority struct{}

func (trustedSystemFileIngestAuthority) revalidate(
	context.Context,
	*gorm.DB,
	*FileService,
	string,
	managev1.UploadType,
	managev1.TranscodeEntityType,
	string,
) error {
	return nil
}

type requestFileIngestAuthority struct{}

func (requestFileIngestAuthority) revalidate(
	ctx context.Context,
	tx *gorm.DB,
	service *FileService,
	_ string,
	uploadType managev1.UploadType,
	entityType managev1.TranscodeEntityType,
	entityID string,
) error {
	return requireFreshFileIngestAuthority(ctx, tx, service, uploadType, entityType, entityID)
}

type multipartFileIngestAuthority struct {
	uploadID string
	fileID   string
}

func newMultipartFileIngestAuthority(session model.UploadSession) (multipartFileIngestAuthority, error) {
	uploadID := strings.TrimSpace(session.UploadID)
	fileID := strings.TrimSpace(session.FileID)
	if uploadID == "" || fileID == "" {
		return multipartFileIngestAuthority{}, fmt.Errorf("multipart upload authority requires stored upload and file IDs")
	}
	return multipartFileIngestAuthority{uploadID: uploadID, fileID: fileID}, nil
}

func (authority multipartFileIngestAuthority) revalidate(
	ctx context.Context,
	tx *gorm.DB,
	service *FileService,
	fileID string,
	uploadType managev1.UploadType,
	entityType managev1.TranscodeEntityType,
	entityID string,
) error {
	if strings.TrimSpace(fileID) != authority.fileID {
		return errs.FailedPrecondition("multipart completion File authority changed")
	}
	var session model.UploadSession
	result := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("upload_id = ? AND file_id = ?", authority.uploadID, authority.fileID).
		Take(&session)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return errs.FailedPrecondition("multipart completion upload authority no longer exists")
	}
	if result.Error != nil {
		return errs.Internal(fmt.Errorf("lock multipart completion upload authority: %w", result.Error))
	}
	if session.Status != model.UploadSessionStatusFinalizing ||
		session.UploadType != uploadType.String() ||
		uploadSessionEntityTypeToEnum(session.EntityType) != entityType ||
		strings.TrimSpace(session.EntityID) != strings.TrimSpace(entityID) {
		return errs.FailedPrecondition("multipart completion upload authority changed")
	}
	return requireFreshFileIngestAuthority(ctx, tx, service, uploadType, entityType, session.EntityID)
}

func requireFreshFileIngestAuthority(
	ctx context.Context,
	tx *gorm.DB,
	service *FileService,
	uploadType managev1.UploadType,
	entityType managev1.TranscodeEntityType,
	entityID string,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return errs.AuthenticationRequired()
	}
	txService := *service
	txService.db = tx
	storedEntityType := ""
	if entityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED {
		storedEntityType = entityType.String()
	}
	session := model.UploadSession{EntityID: strings.TrimSpace(entityID)}
	if storedEntityType != "" {
		session.EntityType = &storedEntityType
	}
	target, resolvedEntityID, err := txService.resolvePartUploadPermissionTarget(ctx, uploadType, session)
	if err != nil {
		return connectUploadPermissionValidationError(err)
	}
	if target.resourceType == "post" {
		access, dependencyErr := requirePostAccess(service.postAccess)
		if dependencyErr != nil {
			return dependencyErr
		}
		return access.RequireLockedEdit(ctx, tx, resolvedEntityID)
	}
	if target.resourceType == "program_event" {
		access, dependencyErr := requireProgramEventAttachment(service.programEventAttachment)
		if dependencyErr != nil {
			return dependencyErr
		}
		return access.RequireLockedEdit(ctx, tx, service.spiceDB, resolvedEntityID)
	}
	if err := lockFileIngestAuthorizationTarget(ctx, tx, target, resolvedEntityID); err != nil {
		return err
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return errs.Internal(fmt.Errorf("lock File ingest principal: %w", err))
	}
	if !active {
		return errs.PermissionDenied("File ingest authority was revoked")
	}
	if err := txService.checkRoleAndSpiceDBUploadPermission(
		ctx,
		target,
		resolvedEntityID,
		principal.MemberID.String(),
	); err != nil {
		return connectUploadPermissionValidationError(err)
	}
	return nil
}

func lockFileIngestAuthorizationTarget(
	ctx context.Context,
	tx *gorm.DB,
	target uploadPermissionTarget,
	entityID string,
) error {
	table, ok := uploadPermissionResourceTable(target.resourceType)
	if !ok || strings.TrimSpace(entityID) == "" {
		return nil
	}
	var row struct {
		ID string `gorm:"column:id"`
	}
	result := tx.WithContext(ctx).
		Table(table).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").
		Where("id = ?", entityID).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return errs.NotFound(target.resourceType, entityID)
	}
	if result.Error != nil {
		return errs.Internal(fmt.Errorf("lock File ingest %s target: %w", target.resourceType, result.Error))
	}
	return nil
}
