package post

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
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const postContentDocumentProfile = "post"

type postContentDocumentRoot struct {
	ID                string           `gorm:"column:id"`
	Status            model.PostStatus `gorm:"column:status"`
	ContentDocumentID *uuid.UUID       `gorm:"column:content_document_id;type:uuid"`
	SourceLocale      string           `gorm:"column:source_locale"`
}

func loadPostContentDocumentID(ctx context.Context, db *gorm.DB, postID string) (uuid.UUID, error) {
	if _, err := uuidutil.ParseCanonical(postID, "post_id"); err != nil {
		return uuid.Nil, errs.InvalidArgument("post_id", "must be a canonical UUID")
	}
	var root postContentDocumentRoot
	if err := db.WithContext(ctx).
		Table("post").
		Select("id", "status", "content_document_id", "source_locale").
		Where("id = ?::uuid", postID).
		Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, errs.NotFound("post", postID)
		}
		return uuid.Nil, errs.Internal(fmt.Errorf("load Post content document: %w", err))
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID == uuid.Nil {
		return uuid.Nil, errs.FailedPrecondition("Post content document has not been populated")
	}
	return *root.ContentDocumentID, nil
}

func lockPostContentDocumentRoot(
	ctx context.Context,
	tx *gorm.DB,
	postID string,
	documentID uuid.UUID,
	allowArchived bool,
) (postContentDocumentRoot, error) {
	var root postContentDocumentRoot
	if err := tx.WithContext(ctx).
		Table("post").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "status", "content_document_id", "source_locale").
		Where("id = ?::uuid", postID).
		Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return postContentDocumentRoot{}, errs.NotFound("post", postID)
		}
		return postContentDocumentRoot{}, errs.Internal(err)
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID != documentID {
		return postContentDocumentRoot{}, errs.FailedPrecondition("Post content document changed; reload before saving")
	}
	if !allowArchived && root.Status == model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String()) {
		return postContentDocumentRoot{}, errs.FailedPrecondition("archived posts are read-only")
	}
	return root, nil
}

func postCollaborationDocumentFence(
	spiceDB *auth.SpiceDBClient,
	postID string,
	contributorMemberIDs []string,
) contentblock.DomainFence {
	contributors := append([]string(nil), contributorMemberIDs...)
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		root, err := lockPostContentDocumentRoot(ctx, tx, postID, documentID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if err := requirePostContributorsPermission(ctx, tx, spiceDB, root.ID, root.Status, contributors); err != nil {
			return contentblock.DomainContext{}, err
		}
		return loadPostContentDomainContext(ctx, tx, postID)
	}
}

func postCreationDocumentFence(
	postID string,
	sourceLocale string,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if _, err := lockPostContentDocumentRoot(ctx, tx, postID, documentID, false); err != nil {
			return contentblock.DomainContext{}, err
		}
		if strings.TrimSpace(sourceLocale) == "" {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Post source locale is invalid")
		}
		return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
	}
}

func authorizePostBlockBootstrap(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB CollaborationPermissionChecker,
	postID string,
	documentID uuid.UUID,
	principalMessage *intrav1.CollaborationPrincipal,
) error {
	if principalMessage == nil {
		return errs.InvalidArgument("principal", "is required")
	}
	root, err := lockPostContentDocumentRoot(ctx, tx, postID, documentID, true)
	if err != nil {
		return err
	}
	principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(
		ctx,
		tx,
		principalMessage.GetSessionId(),
	)
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return errs.AuthenticationRequired()
	}
	if err != nil {
		return errs.Internal(fmt.Errorf("resolve Post collaboration principal: %w", err))
	}
	if principal == nil || principal.Banned || !principal.Onboarded {
		return errs.NoPermission("edit", "post")
	}
	principalContext := auth.WithUser(ctx, principal)
	if root.Status == model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String()) {
		can, canErr := policyv1.Post.ViewArchived(postID)
		if canErr != nil {
			return errs.InvalidArgument("post_id", "must be a canonical UUID")
		}
		decision, decisionErr := auth.AuthorizationDecision(principalContext, can)
		if decisionErr != nil {
			return errs.AuthenticationRequired()
		}
		allowed, checkErr := spiceDB.Can(ctx, decision)
		if checkErr != nil {
			return errs.DependencyUnavailable("SpiceDB")
		}
		if !allowed {
			return errs.NoPermission("view", "post")
		}
		return nil
	}
	can, err := policyv1.Post.Edit(postID)
	if err != nil {
		return errs.InvalidArgument("post_id", "must be a canonical UUID")
	}
	decision, err := auth.AuthorizationDecision(principalContext, can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := spiceDB.Can(ctx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NoPermission("edit", "post")
	}
	return nil
}

func postLockedDocumentFence(
	postID string,
	allowArchived bool,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		root, err := lockPostContentDocumentRoot(ctx, tx, postID, documentID, allowArchived)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		return loadPostContentDomainContext(ctx, tx, root.ID)
	}
}

func postSystemTranslationDocumentFence(postID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		// An accepted job may finish after archival; a deleted root is still
		// rejected by the locked lookup.
		if _, err := lockPostContentDocumentRoot(ctx, tx, postID, documentID, true); err != nil {
			return contentblock.DomainContext{}, err
		}
		return loadPostContentDomainContext(ctx, tx, postID)
	}
}

func postAuthorizedDocumentFence(
	documentID uuid.UUID,
	domain contentblock.DomainContext,
) contentblock.DomainFence {
	return func(_ context.Context, _ *gorm.DB, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
		if requestedDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Post content document changed; reload before saving")
		}
		return domain, nil
	}
}

func loadPostContentDomainContext(
	ctx context.Context,
	db *gorm.DB,
	postID string,
) (contentblock.DomainContext, error) {
	var row struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := db.WithContext(ctx).
		Table("post").
		Select("source_locale").
		Where("id = ?::uuid", postID).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Post source locale has not been initialized")
		}
		return contentblock.DomainContext{}, errs.Internal(err)
	}
	if strings.TrimSpace(row.SourceLocale) == "" {
		return contentblock.DomainContext{}, errs.FailedPrecondition("Post source locale is invalid")
	}
	return contentblock.DomainContext{SourceLocale: row.SourceLocale}, nil
}

func parsePostContentUUID(field, value string) (uuid.UUID, error) {
	parsed, err := uuidutil.ParseCanonical(value, field)
	if err != nil {
		return uuid.Nil, errs.InvalidArgument(field, "must be a canonical UUID")
	}
	return parsed, nil
}

func normalizePostContentBlockError(err error) error {
	if err == nil || connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	var targetConflict *translation.TargetRevisionConflict
	switch {
	case errors.As(err, &targetConflict):
		return errs.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			"Post target locale changed since it was loaded; reload before saving",
		)
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return errs.NotFoundMsg("Post content document not found")
	case errors.Is(err, contentblock.ErrStaleRevision):
		return errs.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			"Post document changed since it was loaded; reload before saving",
		)
	case errors.Is(err, contentblock.ErrCrossDocument):
		return errs.InvalidArgument("blocks", "a Block belongs to another document")
	case errors.Is(err, contentblock.ErrFileReference):
		return errs.FailedPrecondition("a Post Block File reference is not attachable")
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return errs.InvalidArgument("blocks", err.Error())
	default:
		return errs.Internal(fmt.Errorf("post content document: %w", err))
	}
}
