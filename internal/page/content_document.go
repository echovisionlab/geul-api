package page

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
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
	return loadPageContentDocumentIDWithLock(ctx, db, pageID, "")
}

func loadPageContentDocumentIDForRead(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
) (uuid.UUID, error) {
	return loadPageContentDocumentIDWithLock(ctx, tx, pageID, clause.LockingStrengthShare)
}

func loadPageContentDocumentIDWithLock(ctx context.Context, db *gorm.DB, pageID, lock string) (uuid.UUID, error) {
	query := db.WithContext(ctx).Table("page")
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	var root pageContentDocumentRoot
	if err := query.Select("id", "content_document_id").Where("id = ?", pageID).Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, domainerr.ResourceNotFound("page", pageID)
		}
		return uuid.Nil, err
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID == uuid.Nil {
		return uuid.Nil, domainerr.Precondition("page content document has not been populated")
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

// authorizePageBlockBootstrap resolves the browser session sessionID to its
// principal and requires it to edit the Page, locking the session for the
// read.
func authorizePageBlockBootstrap(
	ctx context.Context,
	db *gorm.DB,
	checker CollaborationPermissionChecker,
	pageID string,
	sessionID string,
) (*auth.UserInfo, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, domainerr.AuthenticationRequired()
	}
	principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(ctx, db, sessionID)
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return nil, domainerr.AuthenticationRequired().WithCause(failurecause.WithAttrs(
			failurecause.WithPrivate(err, sessionID), slog.String("page_id", pageID),
		))
	}
	if err != nil {
		return nil, fmt.Errorf("resolve Page collaboration principal: %w", err)
	}
	if err := requireActiveBootstrapPrincipal(ctx, db, principal); err != nil {
		return nil, err
	}
	if err := requireCollaborationEditForPrincipal(ctx, checker, pageID, principal); err != nil {
		return nil, err
	}
	if err := auth.LockAuthenticatedSessionForPrincipal(ctx, db, sessionID, principal); err != nil {
		if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
			return nil, domainerr.AuthenticationRequired().WithCause(failurecause.WithPrivate(err, sessionID))
		}
		return nil, fmt.Errorf("lock Page collaboration session: %w", err)
	}
	return principal, nil
}

// requireActiveBootstrapPrincipal admits an authenticated, onboarded and
// active principal that is not banned.
func requireActiveBootstrapPrincipal(ctx context.Context, db *gorm.DB, principal *auth.UserInfo) error {
	if principal == nil || !principal.Authenticated {
		return domainerr.AuthenticationRequired()
	}
	if principal.Banned {
		return domainerr.AccountBanned()
	}
	if !principal.Onboarded {
		return domainerr.NoPermission("edit", "page")
	}
	active, err := identitystate.LockActivePrincipal(ctx, db, principal)
	if err != nil {
		return err
	}
	if !active {
		return domainerr.NoPermission("edit", "page")
	}
	return nil
}

func lockPageContentDocumentRoot(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
	documentID uuid.UUID,
) error {
	var root pageContentDocumentRoot
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Table("page").
		Select("id", "content_document_id").
		Where("id = ?", pageID).
		Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("page", pageID)
		}
		return err
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID != documentID {
		return domainerr.Precondition("page content document changed; reload before saving")
	}
	return nil
}

// pageCollaborationDocumentFence locks the Page root of documentID and
// requires every contributor to hold action on it.
func (s *Service) pageCollaborationDocumentFence(
	pageID string,
	action pageAction,
	contributorMemberIDs []string,
) contentblock.DomainFence {
	contributors := append([]string(nil), contributorMemberIDs...)
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockPageContentDocumentRoot(ctx, tx, pageID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		if err := requirePageContributorsPermission(ctx, tx, s.permissions, pageID, action, contributors); err != nil {
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
			return contentblock.DomainContext{}, domainerr.ResourceNotFound("page", pageID)
		}
		return contentblock.DomainContext{}, err
	}
	locale := strings.TrimSpace(source.SourceLocale)
	if locale == "" {
		return contentblock.DomainContext{}, domainerr.Precondition("page translation source locale is missing")
	}
	return contentblock.DomainContext{SourceLocale: locale}, nil
}

func parsePageContentUUID(fieldName string, value string) (uuid.UUID, error) {
	normalized := strings.TrimSpace(value)
	parsed, err := uuid.Parse(normalized)
	if err != nil || parsed == uuid.Nil || parsed.String() != normalized {
		return uuid.Nil, domainerr.InvalidField(fieldName, "must be a canonical UUID")
	}
	return parsed, nil
}

// normalizePageContentBlockError answers a content store failure. An error
// a shared helper or this package already answered passes through.
func normalizePageContentBlockError(err error) error {
	if err == nil || mutation.IsClassified(err) {
		return err
	}
	var targetConflict *translation.TargetRevisionConflict
	if errors.As(err, &targetConflict) {
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			"Page target translation changed since it was loaded; reload before saving", domainerr.CollaborationRevisionsOf(err),
		).WithCause(err)
	}
	switch {
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return domainerr.New(domainerr.NotFound, "", "page content document not found").WithCause(err)
	case errors.Is(err, contentblock.ErrStaleRevision):
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			"Page document changed since it was loaded; reload before saving", domainerr.CollaborationRevisionsOf(err),
		).WithCause(err)
	case errors.Is(err, contentblock.ErrCrossDocument):
		return domainerr.InvalidField("blocks", "a Block belongs to another document").WithCause(err)
	case errors.Is(err, contentblock.ErrFileReference):
		return domainerr.Precondition("a Page Block File reference is not attachable").WithCause(err)
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return domainerr.InvalidField("blocks", err.Error())
	default:
		return fmt.Errorf("page content document: %w", err)
	}
}
