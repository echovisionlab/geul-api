package post

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const postContentDocumentProfile = "post"

// postEntityName names the owner in shared content document messages.
const postEntityName = "Post"

func loadPostContentDocumentID(ctx context.Context, store Store, postID string) (uuid.UUID, error) {
	if _, err := uuidutil.ParseCanonical(postID, "post_id"); err != nil {
		return uuid.Nil, domainerr.InvalidField("post_id", "must be a canonical UUID").WithCause(err)
	}
	root, err := store.DocumentRoot(ctx, postID)
	if err != nil {
		if errors.Is(err, ErrPostNotFound) {
			return uuid.Nil, domainerr.ResourceNotFound("post", postID)
		}
		return uuid.Nil, fmt.Errorf("load Post content document: %w", err)
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID == uuid.Nil {
		return uuid.Nil, domainerr.Precondition("Post content document has not been populated")
	}
	return *root.ContentDocumentID, nil
}

func lockPostContentDocumentRoot(
	ctx context.Context,
	store Store,
	postID string,
	documentID uuid.UUID,
	allowArchived bool,
) (DocumentRoot, error) {
	root, err := store.LockDocumentRoot(ctx, postID)
	if err != nil {
		if errors.Is(err, ErrPostNotFound) {
			return DocumentRoot{}, domainerr.ResourceNotFound("post", postID)
		}
		return DocumentRoot{}, err
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID != documentID {
		return DocumentRoot{}, domainerr.Precondition("Post content document changed; reload before saving")
	}
	if !allowArchived && root.Status == StatusArchived {
		return DocumentRoot{}, domainerr.Precondition("archived posts are read-only")
	}
	return root, nil
}

// lockedContentFence admits only documentID and answers with the domain
// context the caller already authorized under its lock
// (contentblock.LockedFence).
func lockedContentFence(documentID uuid.UUID, domain contentblock.DomainContext) ContentFence {
	return func(_ context.Context, _ Store, requested uuid.UUID) (contentblock.DomainContext, error) {
		if requested != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition(postEntityName + " content document changed; reload before saving")
		}
		return domain, nil
	}
}

func postCollaborationDocumentFence(
	spiceDB Authorizer,
	postID string,
	contributorMemberIDs []string,
) ContentFence {
	contributors := append([]string(nil), contributorMemberIDs...)
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		root, err := lockPostContentDocumentRoot(ctx, store, postID, documentID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if err := requirePostContributorsPermission(ctx, store, spiceDB, root.ID, root.Status, contributors); err != nil {
			return contentblock.DomainContext{}, err
		}
		return loadPostContentDomainContext(ctx, store, postID)
	}
}

func postCreationDocumentFence(
	postID string,
	sourceLocale string,
) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if _, err := lockPostContentDocumentRoot(ctx, store, postID, documentID, false); err != nil {
			return contentblock.DomainContext{}, err
		}
		if strings.TrimSpace(sourceLocale) == "" {
			return contentblock.DomainContext{}, domainerr.Precondition("Post source locale is invalid")
		}
		return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
	}
}

func authorizePostBlockBootstrap(
	ctx context.Context,
	store Store,
	checker authz.AuthorizationDecisionChecker,
	postID string,
	documentID uuid.UUID,
	sessionID *string,
) (*auth.UserInfo, error) {
	if sessionID == nil {
		return nil, domainerr.InvalidField("principal", "is required")
	}
	root, err := lockPostContentDocumentRoot(ctx, store, postID, documentID, true)
	if err != nil {
		return nil, err
	}
	principal, err := store.ResolveSessionPrincipal(ctx, *sessionID)
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return nil, domainerr.AuthenticationRequired()
	}
	if err != nil {
		return nil, fmt.Errorf("resolve Post collaboration principal: %w", err)
	}
	if principal == nil || principal.Banned || !principal.Onboarded {
		return nil, domainerr.NoPermission("edit", "post")
	}
	principalContext := auth.WithUser(ctx, principal)
	if root.Status == StatusArchived {
		can, canErr := policyv1.Post.ViewArchived(postID)
		if canErr != nil {
			return nil, domainerr.InvalidField("post_id", "must be a canonical UUID").WithCause(canErr)
		}
		decision, decisionErr := auth.AuthorizationDecision(principalContext, can)
		if decisionErr != nil {
			return nil, domainerr.AuthenticationRequired().WithCause(decisionErr)
		}
		allowed, checkErr := checker.Can(ctx, decision)
		if checkErr != nil {
			return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(checkErr)
		}
		if !allowed {
			return nil, domainerr.NoPermission("view", "post")
		}
		return principal, nil
	}
	can, err := policyv1.Post.Edit(postID)
	if err != nil {
		return nil, domainerr.InvalidField("post_id", "must be a canonical UUID").WithCause(err)
	}
	decision, err := auth.AuthorizationDecision(principalContext, can)
	if err != nil {
		return nil, domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return nil, domainerr.NoPermission("edit", "post")
	}
	return principal, nil
}

func postLockedDocumentFence(
	postID string,
	allowArchived bool,
) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		root, err := lockPostContentDocumentRoot(ctx, store, postID, documentID, allowArchived)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		return loadPostContentDomainContext(ctx, store, root.ID)
	}
}

func postSystemTranslationDocumentFence(postID string) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		// An accepted job may finish after archival; a deleted root is still
		// rejected by the locked lookup.
		if _, err := lockPostContentDocumentRoot(ctx, store, postID, documentID, true); err != nil {
			return contentblock.DomainContext{}, err
		}
		return loadPostContentDomainContext(ctx, store, postID)
	}
}

func loadPostContentDomainContext(
	ctx context.Context,
	store Store,
	postID string,
) (contentblock.DomainContext, error) {
	sourceLocale, err := store.SourceLocale(ctx, postID)
	if err != nil {
		if errors.Is(err, ErrPostNotFound) {
			return contentblock.DomainContext{}, domainerr.Precondition("Post source locale has not been initialized")
		}
		return contentblock.DomainContext{}, err
	}
	if strings.TrimSpace(sourceLocale) == "" {
		return contentblock.DomainContext{}, domainerr.Precondition("Post source locale is invalid")
	}
	return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
}

func parsePostContentUUID(field, value string) (uuid.UUID, error) {
	parsed, err := uuidutil.ParseCanonical(value, field)
	if err != nil {
		return uuid.Nil, domainerr.InvalidField(field, "must be a canonical UUID").WithCause(err)
	}
	return parsed, nil
}

func normalizePostContentBlockError(err error) error {
	if err == nil || mutation.IsClassified(err) {
		return err
	}
	var targetConflict *translation.TargetRevisionConflict
	switch {
	case errors.As(err, &targetConflict):
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			"Post target locale changed since it was loaded; reload before saving", domainerr.CollaborationRevisionsOf(err),
		).WithCause(err)
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return domainerr.New(domainerr.NotFound, "", "Post content document not found").WithCause(err)
	case errors.Is(err, contentblock.ErrStaleRevision):
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			"Post document changed since it was loaded; reload before saving", domainerr.CollaborationRevisionsOf(err),
		).WithCause(err)
	case errors.Is(err, contentblock.ErrCrossDocument):
		return domainerr.InvalidField("blocks", "a Block belongs to another document").WithCause(err)
	case errors.Is(err, contentblock.ErrFileReference):
		return domainerr.Precondition("a Post Block File reference is not attachable").WithCause(err)
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return domainerr.InvalidField("blocks", err.Error())
	default:
		return fmt.Errorf("post content document: %w", err)
	}
}
