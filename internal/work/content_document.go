package work

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
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
		return uuid.Nil, domainerr.InvalidField(fieldName, "must be a canonical UUID")
	}
	return parsed, nil
}

func normalizeWorkContentBlockError(err error) error {
	if err == nil || mutation.IsClassified(err) {
		return err
	}
	// A revision conflict keeps its cause, so a conflict found while storing
	// takes the same errors.Is/As branches (AI document conflict result,
	// translation source no longer current) as one found by the pre-check.
	var targetConflict *translation.TargetRevisionConflict
	if errors.As(err, &targetConflict) {
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			targetConflict.Error(), domainerr.CollaborationRevisionsOf(err),
		).WithCause(err)
	}

	switch {
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return domainerr.New(domainerr.NotFound, "", "work content document not found").WithCause(err)
	case errors.Is(err, contentblock.ErrStaleRevision):
		return domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			"work content revision changed; reload before saving", domainerr.CollaborationRevisionsOf(err),
		).WithCause(err)
	case errors.Is(err, contentblock.ErrCrossDocument):
		return domainerr.InvalidField("blocks", "a Block belongs to another document").WithCause(err)
	case errors.Is(err, contentblock.ErrFileReference):
		// The cause stays out of the chain: AI document callers would turn
		// ErrFileReference into an operation issue, and Work has always
		// answered FailedPrecondition here.
		return domainerr.Precondition("a Work Block File reference is not attachable")
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return domainerr.InvalidField("blocks", err.Error())
	default:
		return fmt.Errorf("work content document: %w", err)
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
		return uuid.Nil, domainerr.ResourceNotFound("work", workID)
	}
	if result.Error != nil {
		return uuid.Nil, result.Error
	}
	if row.ContentDocumentID == nil || strings.TrimSpace(*row.ContentDocumentID) == "" {
		return uuid.Nil, domainerr.Precondition("work content document is not initialized")
	}
	documentID, err := uuid.Parse(*row.ContentDocumentID)
	if err != nil {
		return uuid.Nil, err
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
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Select("id", "status", "content_document_id", "source_locale").
		Where("id = ?", workID).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return uuid.Nil, domainerr.ResourceNotFound("work", workID)
	}
	if result.Error != nil {
		return uuid.Nil, result.Error
	}
	if row.ContentDocumentID == nil || strings.TrimSpace(*row.ContentDocumentID) == "" {
		return uuid.Nil, domainerr.Precondition("work content document is not initialized")
	}
	documentID, err := uuid.Parse(*row.ContentDocumentID)
	if err != nil {
		return uuid.Nil, err
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
	sessionID string,
) (*auth.UserInfo, error) {
	if sessionID == "" {
		return nil, domainerr.AuthenticationRequired()
	}
	work, err := lockWorkForContentDocument(ctx, db, documentID)
	if err != nil {
		return nil, err
	}
	if work.ID != workID {
		return nil, domainerr.Precondition("Work content document changed; reload before editing")
	}
	principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(
		ctx,
		db,
		sessionID,
	)
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return nil, domainerr.AuthenticationRequired().WithCause(err)
	}
	if err != nil {
		return nil, err
	}
	if principal == nil || !principal.Authenticated {
		return nil, domainerr.AuthenticationRequired()
	}
	if principal.Banned {
		return nil, domainerr.AccountBanned()
	}
	if !principal.Onboarded {
		return nil, domainerr.NoPermission("edit", "work")
	}
	active, err := identitystate.LockActivePrincipal(ctx, db, principal)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, domainerr.NoPermission("edit", "work")
	}
	action := workLifecycleAction(work.Status, policyv1.Work.Edit, workAuthorizationRead)
	if err := requireWorkPermissionForCurrentActor(auth.WithUser(ctx, principal), spiceDB, workID, action); err != nil {
		return nil, err
	}
	if err := auth.LockAuthenticatedSessionForPrincipal(ctx, db, sessionID, principal); err != nil {
		if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
			return nil, domainerr.AuthenticationRequired().WithCause(err)
		}
		return nil, fmt.Errorf("lock Work collaboration session: %w", err)
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
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "status", "content_document_id", "source_locale").
		Where("content_document_id = ?", documentID).
		Take(&work)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return lockedWorkContentRoot{}, domainerr.ResourceNotFound("work", documentID.String())
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
			return contentblock.DomainContext{}, domainerr.Precondition("Work content document changed during creation")
		}
		if strings.TrimSpace(sourceLocale) == "" {
			return contentblock.DomainContext{}, domainerr.Precondition("Work source locale is invalid")
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
		return contentblock.DomainContext{}, domainerr.Precondition("work translation source is not initialized")
	}
	if result.Error != nil {
		return contentblock.DomainContext{}, result.Error
	}
	if strings.TrimSpace(row.SourceLocale) == "" {
		return contentblock.DomainContext{}, domainerr.Precondition("work source locale is empty")
	}
	return contentblock.DomainContext{SourceLocale: row.SourceLocale}, nil
}
