package filemedia

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type verifiedFileIngestAuthority interface {
	revalidate(
		context.Context,
		Store,
		*Service,
		string,
		managev1.UploadType,
		managev1.TranscodeEntityType,
		string,
		func(*auth.UserInfo) (bool, error),
	) error
}

// trustedSystemFileIngestAuthority is reserved for authenticated internal
// worker/system ingress that has no request actor. Request paths must use an
// actor-bearing authority fence.
type trustedSystemFileIngestAuthority struct{}

func (trustedSystemFileIngestAuthority) revalidate(
	context.Context,
	Store,
	*Service,
	string,
	managev1.UploadType,
	managev1.TranscodeEntityType,
	string,
	func(*auth.UserInfo) (bool, error),
) error {
	return nil
}

type requestFileIngestAuthority struct{}

func (requestFileIngestAuthority) revalidate(
	ctx context.Context,
	tx Store,
	service *Service,
	_ string,
	uploadType managev1.UploadType,
	entityType managev1.TranscodeEntityType,
	entityID string,
	lockActive func(*auth.UserInfo) (bool, error),
) error {
	return requireFreshFileIngestAuthorityInStore(ctx, tx, service, uploadType, entityType, entityID, lockActive)
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
	tx Store,
	service *Service,
	fileID string,
	uploadType managev1.UploadType,
	entityType managev1.TranscodeEntityType,
	entityID string,
	lockActive func(*auth.UserInfo) (bool, error),
) error {
	if strings.TrimSpace(fileID) != authority.fileID {
		return domainerr.Precondition("multipart completion File authority changed")
	}
	session, err := tx.UploadSession(ctx, authority.uploadID, authority.fileID, true)
	if errors.Is(err, domain.ErrUploadSessionNotFound) {
		return domainerr.Precondition("multipart completion upload authority no longer exists")
	}
	if err != nil {
		return fmt.Errorf("lock multipart completion upload authority: %w", err)
	}

	if session.Status != domain.UploadSessionStatusFinalizing ||
		session.UploadType != uploadType.String() ||
		uploadSessionEntityTypeToEnum(session.EntityType) != entityType ||
		strings.TrimSpace(session.EntityID) != strings.TrimSpace(entityID) {
		return domainerr.Precondition("multipart completion upload authority changed")
	}
	return requireFreshFileIngestAuthorityInStore(ctx, tx, service, uploadType, entityType, session.EntityID, lockActive)
}

func requireFreshFileIngestAuthorityInStore(ctx context.Context, store Store, service *Service, uploadType managev1.UploadType, entityType managev1.TranscodeEntityType, entityID string, lockActive func(*auth.UserInfo) (bool, error)) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return domainerr.AuthenticationRequired()
	}
	session := model.UploadSession{EntityID: strings.TrimSpace(entityID)}
	if entityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED {
		session.EntityType = new(entityType.String())
	}
	target, resolvedID, err := resolvePartUploadPermissionTargetUsing(ctx, uploadType, session, func(ctx context.Context, id string) (string, error) {
		rows, err := store.TrackReleaseIDs(ctx, []string{id})
		if err != nil {
			return "", err
		}
		release, ok := rows[id]
		if !ok {
			return "", domain.ErrUploadTrackNotFound
		}
		return release, nil
	})
	if err != nil {
		return connectUploadPermissionValidationError(err)
	}
	if target.resourceType == "post" || target.resourceType == "program_event" {
		return store.RequirePolicyOwner(ctx, target.resourceType, resolvedID, true)
	}
	if err := store.LockIngestOwner(ctx, target.resourceType, resolvedID); err != nil {
		return err
	}
	active, err := lockActive(principal)
	if err != nil {
		return fmt.Errorf("lock File ingest principal: %w", err)
	}
	if !active {
		return domainerr.Denied("File ingest authority was revoked")
	}
	if err := service.checkRoleAndSpiceDBUploadPermission(ctx, target, resolvedID, principal.MemberID.String()); err != nil {
		return connectUploadPermissionValidationError(err)
	}
	return nil
}

func requireFreshFileIngestAuthority(
	ctx context.Context,
	tx *gorm.DB,
	service *Service,
	uploadType managev1.UploadType,
	entityType managev1.TranscodeEntityType,
	entityID string,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return domainerr.AuthenticationRequired()
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
		access := service.postAccess
		return access.RequireLockedEdit(ctx, tx, resolvedEntityID)
	}
	if target.resourceType == "program_event" {
		access := service.programEventAttachment
		return access.RequireLockedEdit(ctx, tx, service.spiceDB, resolvedEntityID)
	}
	if err := lockFileIngestAuthorizationTarget(ctx, tx, target, resolvedEntityID); err != nil {
		return err
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return fmt.Errorf("lock File ingest principal: %w", err)
	}
	if !active {
		return domainerr.Denied("File ingest authority was revoked")
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
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id").
		Where("id = ?", entityID).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return domainerr.ResourceNotFound(target.resourceType, entityID)
	}
	if result.Error != nil {
		return fmt.Errorf("lock File ingest %s target: %w", target.resourceType, result.Error)
	}
	return nil
}
