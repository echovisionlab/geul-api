package filemedia

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

type verifiedFileIngestAuthority interface {
	revalidate(
		context.Context,
		Store,
		*Service,
		string,
		UploadType,
		EntityType,
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
	UploadType,
	EntityType,
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
	uploadType UploadType,
	entityType EntityType,
	entityID string,
	lockActive func(*auth.UserInfo) (bool, error),
) error {
	return requireFreshFileIngestAuthority(ctx, tx, service, uploadType, entityType, entityID, lockActive)
}

type multipartFileIngestAuthority struct {
	uploadID string
	fileID   string
}

func newMultipartFileIngestAuthority(session domain.UploadSession) (multipartFileIngestAuthority, error) {
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
	uploadType UploadType,
	entityType EntityType,
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
	return requireFreshFileIngestAuthority(ctx, tx, service, uploadType, entityType, session.EntityID, lockActive)
}

func requireFreshFileIngestAuthority(ctx context.Context, store Store, service *Service, uploadType UploadType, entityType EntityType, entityID string, lockActive func(*auth.UserInfo) (bool, error)) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return domainerr.AuthenticationRequired()
	}
	session := domain.UploadSession{EntityID: strings.TrimSpace(entityID)}
	if entityType != EntityUnspecified {
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
