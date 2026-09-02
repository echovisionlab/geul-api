package page

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

const pageContentDocumentProfile = "page"

func MaterializePageContentDocument(
	snapshot contentblock.Snapshot,
) (*contentv1.PageDocument, error) {
	return contentblock.SnapshotToPageDocument(snapshot)
}

func MaterializeLocalizedPageContentDocument(
	snapshot contentblock.Snapshot,
	locale string,
) (*contentv1.LocalizedPageDocument, error) {
	return contentblock.MaterializeSnapshotPageLocale(snapshot, locale)
}

type pageContentDocumentRoot struct {
	ID                string     `gorm:"column:id"`
	ContentDocumentID *uuid.UUID `gorm:"column:content_document_id;type:uuid"`
}

func loadPageContentDocumentID(
	ctx context.Context,
	db *gorm.DB,
	pageID string,
) (uuid.UUID, error) {
	var root pageContentDocumentRoot
	if err := db.WithContext(ctx).
		Table("page").
		Select("id", "content_document_id").
		Where("id = ?", pageID).
		Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, errs.NotFound("page", pageID)
		}
		return uuid.Nil, errs.Internal(err)
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID == uuid.Nil {
		return uuid.Nil, errs.FailedPrecondition("page content document has not been populated")
	}
	return *root.ContentDocumentID, nil
}

func loadPageContentDocumentIDForRead(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
) (uuid.UUID, error) {
	var root pageContentDocumentRoot
	if err := tx.WithContext(ctx).
		Table("page").
		Clauses(clause.Locking{Strength: "SHARE"}).
		Select("id", "content_document_id").
		Where("id = ?", pageID).
		Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, errs.NotFound("page", pageID)
		}
		return uuid.Nil, errs.Internal(err)
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID == uuid.Nil {
		return uuid.Nil, errs.FailedPrecondition("page content document has not been populated")
	}
	return *root.ContentDocumentID, nil
}

func LoadPageContentDocumentIDForPublicRead(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
) (uuid.UUID, error) {
	return loadPageContentDocumentIDForRead(ctx, tx, pageID)
}

func authorizePageBlockBootstrap(
	ctx context.Context,
	db *gorm.DB,
	checker CollaborationPermissionChecker,
	pageID string,
	principalMessage *intrav1.CollaborationPrincipal,
) (*auth.UserInfo, error) {
	if principalMessage == nil || strings.TrimSpace(principalMessage.GetSessionId()) == "" {
		return nil, errs.AuthenticationRequired()
	}
	principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(
		ctx,
		db,
		principalMessage.GetSessionId(),
	)
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return nil, errs.AuthenticationRequired()
	}
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("resolve Page collaboration principal: %w", err))
	}
	if principal == nil || !principal.Authenticated {
		return nil, errs.AuthenticationRequired()
	}
	if principal.Banned {
		return nil, errs.AccountBanned()
	}
	if !principal.Onboarded {
		return nil, errs.NoPermission("edit", "page")
	}
	active, err := identitystate.LockActivePrincipal(ctx, db, principal)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if !active {
		return nil, errs.NoPermission("edit", "page")
	}
	if err := RequireCollaborationEditForPrincipal(
		ctx,
		checker,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PAGE,
		pageID,
		principal,
	); err != nil {
		return nil, err
	}
	if err := auth.LockAuthenticatedSessionForPrincipal(ctx, db, principalMessage.GetSessionId(), principal); err != nil {
		if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
			return nil, errs.AuthenticationRequired()
		}
		return nil, errs.Internal(fmt.Errorf("lock Page collaboration session: %w", err))
	}
	return principal, nil
}

func lockPageContentDocumentRoot(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
	documentID uuid.UUID,
) error {
	var root pageContentDocumentRoot
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Table("page").
		Select("id", "content_document_id").
		Where("id = ?", pageID).
		Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("page", pageID)
		}
		return errs.Internal(err)
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID != documentID {
		return errs.FailedPrecondition("page content document changed; reload before saving")
	}
	return nil
}

func pageCollaborationDocumentFence(
	spiceDB CollaborationPermissionChecker,
	pageID string,
	action pageAction,
	contributorMemberIDs []string,
) contentblock.DomainFence {
	contributors := append([]string(nil), contributorMemberIDs...)
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockPageContentDocumentRoot(ctx, tx, pageID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		if err := requirePageContributorsPermission(ctx, tx, spiceDB, pageID, action, contributors); err != nil {
			return contentblock.DomainContext{}, err
		}
		return loadPageContentDomainContext(ctx, tx, pageID)
	}
}

func lockedPageContentFence(
	pageID string,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockPageContentDocumentRoot(ctx, tx, pageID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		return loadPageContentDomainContext(ctx, tx, pageID)
	}
}

func loadPageContentDomainContext(
	ctx context.Context,
	db *gorm.DB,
	pageID string,
) (contentblock.DomainContext, error) {
	var source struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := db.WithContext(ctx).
		Table("page").
		Select("source_locale").
		Where("id = ?", pageID).
		Take(&source).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return contentblock.DomainContext{}, errs.NotFound("page", pageID)
		}
		return contentblock.DomainContext{}, errs.Internal(err)
	}
	locale := strings.TrimSpace(source.SourceLocale)
	if locale == "" {
		return contentblock.DomainContext{}, errs.FailedPrecondition("page translation source locale is missing")
	}
	return contentblock.DomainContext{SourceLocale: locale}, nil
}

func parsePageContentUUID(fieldName string, value string) (uuid.UUID, error) {
	normalized := strings.TrimSpace(value)
	parsed, err := uuid.Parse(normalized)
	if err != nil || parsed == uuid.Nil || parsed.String() != normalized {
		return uuid.Nil, errs.InvalidArgument(fieldName, "must be a canonical UUID")
	}
	return parsed, nil
}

func normalizePageContentBlockError(err error) error {
	if err == nil || connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	switch {
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return errs.NotFoundMsg("page content document not found")
	case errors.Is(err, contentblock.ErrStaleRevision):
		return errs.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			"Page document changed since it was loaded; reload before saving",
		)
	case errors.Is(err, contentblock.ErrCrossDocument):
		return errs.InvalidArgument("blocks", "a Block belongs to another document")
	case errors.Is(err, contentblock.ErrFileReference):
		return errs.FailedPrecondition("a Page Block File reference is not attachable")
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return errs.InvalidArgument("blocks", err.Error())
	default:
		return errs.Internal(fmt.Errorf("page content document: %w", err))
	}
}
