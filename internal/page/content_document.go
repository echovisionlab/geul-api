package page

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

const pageContentDocumentProfile = "page"

// loadPageContentDocumentID reads the Page's content document with lock; a
// missing Page answers not found.
func loadPageContentDocumentID(ctx context.Context, store Store, pageID string, lock RowLock) (uuid.UUID, error) {
	root, err := store.DocumentRoot(ctx, pageID, lock)
	if err != nil {
		if errors.Is(err, ErrPageNotFound) {
			return uuid.Nil, domainerr.ResourceNotFound("page", pageID)
		}
		return uuid.Nil, err
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID == uuid.Nil {
		return uuid.Nil, domainerr.Precondition("page content document has not been populated")
	}
	return *root.ContentDocumentID, nil
}

// readPageContentDocumentID is loadPageContentDocumentID in its own read
// snapshot, for a use case that names the document before its mutation.
func (s *Service) readPageContentDocumentID(ctx context.Context, pageID string) (uuid.UUID, error) {
	var documentID uuid.UUID
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		documentID, err = loadPageContentDocumentID(ctx, store, pageID, Unlocked)
		return err
	})
	return documentID, err
}

// authorizePageBlockBootstrap resolves the browser session sessionID to its
// principal and requires it to edit the Page, locking the session for the
// read.
func authorizePageBlockBootstrap(
	ctx context.Context,
	store Store,
	checker authz.AuthorizationDecisionChecker,
	pageID string,
	sessionID string,
) (*auth.UserInfo, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, domainerr.AuthenticationRequired()
	}
	principal, err := store.ResolveSessionPrincipal(ctx, sessionID)
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return nil, domainerr.AuthenticationRequired().WithCause(failurecause.WithAttrs(
			failurecause.WithPrivate(err, sessionID), slog.String("page_id", pageID),
		))
	}
	if err != nil {
		return nil, fmt.Errorf("resolve Page collaboration principal: %w", err)
	}
	if err := requireActiveBootstrapPrincipal(ctx, store, principal); err != nil {
		return nil, err
	}
	if err := requireCollaborationEditForPrincipal(ctx, checker, pageID, principal); err != nil {
		return nil, err
	}
	if err := store.LockSession(ctx, sessionID, principal); err != nil {
		if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
			return nil, domainerr.AuthenticationRequired().WithCause(failurecause.WithPrivate(err, sessionID))
		}
		return nil, fmt.Errorf("lock Page collaboration session: %w", err)
	}
	return principal, nil
}

// requireActiveBootstrapPrincipal admits an authenticated, onboarded and
// active principal that is not banned.
func requireActiveBootstrapPrincipal(ctx context.Context, store Store, principal *auth.UserInfo) error {
	if principal == nil || !principal.Authenticated {
		return domainerr.AuthenticationRequired()
	}
	if principal.Banned {
		return domainerr.AccountBanned()
	}
	if !principal.Onboarded {
		return domainerr.NoPermission("edit", "page")
	}
	active, err := store.LockActivePrincipal(ctx, principal)
	if err != nil {
		return err
	}
	if !active {
		return domainerr.NoPermission("edit", "page")
	}
	return nil
}

func lockPageContentDocumentRoot(ctx context.Context, store Store, pageID string, documentID uuid.UUID) error {
	root, err := store.DocumentRoot(ctx, pageID, UpdateLock)
	if err != nil {
		if errors.Is(err, ErrPageNotFound) {
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
) ContentFence {
	contributors := append([]string(nil), contributorMemberIDs...)
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockPageContentDocumentRoot(ctx, store, pageID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		if err := s.requirePageContributorsPermission(ctx, store, pageID, action, contributors); err != nil {
			return contentblock.DomainContext{}, err
		}
		return loadPageContentDomainContext(ctx, store, pageID)
	}
}

// lockedPageContentFence locks the Page root of documentID and names its
// source locale.
func lockedPageContentFence(pageID string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockPageContentDocumentRoot(ctx, store, pageID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		return loadPageContentDomainContext(ctx, store, pageID)
	}
}

// lockedContentFence admits only documentID and answers with the domain
// context the caller already authorized under its lock
// (contentblock.LockedFence).
func lockedContentFence(documentID uuid.UUID, domain contentblock.DomainContext) ContentFence {
	return func(_ context.Context, _ Store, requested uuid.UUID) (contentblock.DomainContext, error) {
		if requested != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition(pageEntityName + " content document changed; reload before saving")
		}
		return domain, nil
	}
}

func loadPageContentDomainContext(ctx context.Context, store Store, pageID string) (contentblock.DomainContext, error) {
	root, err := store.DocumentRoot(ctx, pageID, Unlocked)
	if err != nil {
		if errors.Is(err, ErrPageNotFound) {
			return contentblock.DomainContext{}, domainerr.ResourceNotFound("page", pageID)
		}
		return contentblock.DomainContext{}, err
	}
	locale := strings.TrimSpace(root.SourceLocale)
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
