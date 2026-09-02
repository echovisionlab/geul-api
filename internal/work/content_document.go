package work

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
)

const workContentDocumentProfile = "work"

func parseWorkContentUUID(fieldName string, value string) (uuid.UUID, error) {
	normalized := strings.TrimSpace(value)
	parsed, err := uuid.Parse(normalized)
	if err != nil || parsed == uuid.Nil || parsed.String() != normalized {
		return uuid.Nil, errs.InvalidArgument(fieldName, "must be a canonical UUID")
	}
	return parsed, nil
}

func normalizeWorkContentBlockError(err error) error {
	if err == nil || connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	switch {
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return errs.NotFoundMsg("work content document not found")
	case errors.Is(err, contentblock.ErrStaleRevision):
		return errs.FailedPrecondition("work content revision changed; reload before saving")
	case errors.Is(err, contentblock.ErrCrossDocument):
		return errs.InvalidArgument("blocks", "a Block belongs to another document")
	case errors.Is(err, contentblock.ErrFileReference):
		return errs.FailedPrecondition("a Work Block File reference is not attachable")
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return errs.InvalidArgument("blocks", err.Error())
	default:
		return errs.Internal(fmt.Errorf("work content document: %w", err))
	}
}

type lockedWorkContentRoot struct {
	ID                string  `gorm:"column:id"`
	Status            string  `gorm:"column:status"`
	ContentDocumentID *string `gorm:"column:content_document_id"`
	SourceLocale      string  `gorm:"column:source_locale"`
}

func loadWorkContentDocumentID(
	ctx context.Context,
	db *gorm.DB,
	workID string,
) (uuid.UUID, error) {
	var row lockedWorkContentRoot
	result := db.WithContext(ctx).
		Table("work").
		Select("id", "status", "content_document_id", "source_locale").
		Where("id = ?", workID).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return uuid.Nil, errs.NotFound("work", workID)
	}
	if result.Error != nil {
		return uuid.Nil, errs.Internal(result.Error)
	}
	if row.ContentDocumentID == nil || strings.TrimSpace(*row.ContentDocumentID) == "" {
		return uuid.Nil, errs.FailedPrecondition("work content document is not initialized")
	}
	documentID, err := uuid.Parse(*row.ContentDocumentID)
	if err != nil {
		return uuid.Nil, errs.Internal(err)
	}
	return documentID, nil
}

func loadWorkContentDocumentIDForRead(
	ctx context.Context,
	tx *gorm.DB,
	workID string,
) (uuid.UUID, error) {
	var row lockedWorkContentRoot
	result := tx.WithContext(ctx).
		Table("work").
		Clauses(clause.Locking{Strength: "SHARE"}).
		Select("id", "status", "content_document_id", "source_locale").
		Where("id = ?", workID).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return uuid.Nil, errs.NotFound("work", workID)
	}
	if result.Error != nil {
		return uuid.Nil, errs.Internal(result.Error)
	}
	if row.ContentDocumentID == nil || strings.TrimSpace(*row.ContentDocumentID) == "" {
		return uuid.Nil, errs.FailedPrecondition("work content document is not initialized")
	}
	documentID, err := uuid.Parse(*row.ContentDocumentID)
	if err != nil {
		return uuid.Nil, errs.Internal(err)
	}
	return documentID, nil
}

func LoadWorkContentDocumentIDForPublicRead(
	ctx context.Context,
	tx *gorm.DB,
	workID string,
) (uuid.UUID, error) {
	return loadWorkContentDocumentIDForRead(ctx, tx, workID)
}

func requireWorkLoadPrincipal(
	ctx context.Context,
	db *gorm.DB,
	spiceDB CollaborationPermissionChecker,
	workID string,
	documentID uuid.UUID,
	principalMessage *intrav1.CollaborationPrincipal,
) (*auth.UserInfo, error) {
	if principalMessage == nil {
		return nil, errs.AuthenticationRequired()
	}
	work, err := lockWorkForContentDocument(ctx, db, documentID)
	if err != nil {
		return nil, err
	}
	if work.ID != workID {
		return nil, errs.FailedPrecondition("Work content document changed; reload before editing")
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
		return nil, errs.Internal(err)
	}
	if principal == nil || !principal.Authenticated {
		return nil, errs.AuthenticationRequired()
	}
	if principal.Banned {
		return nil, errs.AccountBanned()
	}
	if !principal.Onboarded {
		return nil, errs.NoPermission("edit", "work")
	}
	active, err := identitystate.LockActivePrincipal(ctx, db, principal)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if !active {
		return nil, errs.NoPermission("edit", "work")
	}
	action := workLifecycleAction(work.Status, policyv1.Work.Edit, workAuthorizationRead)
	if err := requireWorkPermissionForCurrentActor(auth.WithUser(ctx, principal), spiceDB, workID, action); err != nil {
		return nil, err
	}
	if err := auth.LockAuthenticatedSessionForPrincipal(ctx, db, principalMessage.GetSessionId(), principal); err != nil {
		if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
			return nil, errs.AuthenticationRequired()
		}
		return nil, errs.Internal(fmt.Errorf("lock Work collaboration session: %w", err))
	}
	return principal, nil
}

func lockWorkForContentDocument(
	ctx context.Context,
	tx *gorm.DB,
	documentID uuid.UUID,
) (lockedWorkContentRoot, error) {
	var work lockedWorkContentRoot
	result := tx.WithContext(ctx).
		Table("work").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "status", "content_document_id", "source_locale").
		Where("content_document_id = ?", documentID).
		Take(&work)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return lockedWorkContentRoot{}, errs.NotFound("work", documentID.String())
	}
	if result.Error != nil {
		return lockedWorkContentRoot{}, result.Error
	}
	return work, nil
}

func internalWorkContentFence(
	checkpoints persistencecheckpoint.ContributorFence,
	contributorMemberIDs []string,
) contentblock.DomainFence {
	contributors := append([]string(nil), contributorMemberIDs...)
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		work, err := lockWorkForContentDocument(ctx, tx, documentID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if err := checkpoints.RequireCurrentContributors(
			ctx,
			tx,
			intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_WORK,
			work.ID,
			contributors,
		); err != nil {
			return contentblock.DomainContext{}, err
		}
		return loadWorkContentDomainContext(ctx, tx, work.ID)
	}
}

func lockedWorkContentFence() contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		work, err := lockWorkForContentDocument(ctx, tx, documentID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		return loadWorkContentDomainContext(ctx, tx, work.ID)
	}
}

func workContentCreationFence(
	workID string,
	sourceLocale string,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		work, err := lockWorkForContentDocument(ctx, tx, documentID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if work.ID != workID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Work content document changed during creation")
		}
		if strings.TrimSpace(sourceLocale) == "" {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Work source locale is invalid")
		}
		return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
	}
}

func loadWorkContentDomainContext(
	ctx context.Context,
	db *gorm.DB,
	workID string,
) (contentblock.DomainContext, error) {
	var row struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	result := db.WithContext(ctx).
		Table("work").
		Select("source_locale").
		Where("id = ?", workID).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return contentblock.DomainContext{}, errs.FailedPrecondition("work translation source is not initialized")
	}
	if result.Error != nil {
		return contentblock.DomainContext{}, result.Error
	}
	if strings.TrimSpace(row.SourceLocale) == "" {
		return contentblock.DomainContext{}, errs.FailedPrecondition("work source locale is empty")
	}
	return contentblock.DomainContext{SourceLocale: row.SourceLocale}, nil
}
