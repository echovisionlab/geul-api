package programevent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const programEventContentProfile = "program_event"

type programEventContentDocumentRoot struct {
	ContentDocumentID sql.NullString `gorm:"column:content_document_id"`
}

func loadProgramEventContentDocumentID(
	ctx context.Context,
	db *gorm.DB,
	eventID string,
	lock bool,
) (uuid.UUID, error) {
	if db == nil {
		return uuid.Nil, errors.New("program Event content document database is required")
	}
	query := db.WithContext(ctx).
		Table("program_event").
		Select("content_document_id").
		Where("id = ?", eventID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var root programEventContentDocumentRoot
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, domainerr.ResourceNotFound("program event", eventID)
		}
		return uuid.Nil, err
	}
	if !root.ContentDocumentID.Valid || root.ContentDocumentID.String == "" {
		return uuid.Nil, domainerr.Precondition("Program Event content document is not initialized")
	}
	documentID, err := uuid.Parse(root.ContentDocumentID.String)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid Program Event content_document_id: %w", err)
	}
	return documentID, nil
}

func programEventContentDocumentFence(
	eventID string,
	authorize func(context.Context, *gorm.DB) error,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		query := tx.WithContext(ctx).
			Table("program_event").
			Select("content_document_id").
			Where("id = ?", eventID).
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
		var root programEventContentDocumentRoot
		if err := query.Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return contentblock.DomainContext{}, domainerr.ResourceNotFound("program event", eventID)
			}
			return contentblock.DomainContext{}, err
		}
		if !root.ContentDocumentID.Valid || root.ContentDocumentID.String != documentID.String() {
			return contentblock.DomainContext{}, domainerr.Precondition("Program Event content document ownership changed; reload before saving")
		}
		if authorize == nil {
			return contentblock.DomainContext{}, errors.New("program Event content document authorization is required")
		}
		if err := authorize(ctx, tx); err != nil {
			return contentblock.DomainContext{}, err
		}
		sourceState, err := loadProgramEventSourceLocale(ctx, tx, eventID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		return contentblock.DomainContext{SourceLocale: sourceState.SourceLocale}, nil
	}
}

func requireProgramEventBlockBootstrap(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB CollaborationPermissionChecker,
	eventID string,
	sessionID *string,
) (uuid.UUID, *auth.UserInfo, error) {
	if sessionID == nil {
		return uuid.Nil, nil, authenticationRequired()
	}
	documentID, err := loadProgramEventContentDocumentID(ctx, tx, eventID, true)
	if err != nil {
		return uuid.Nil, nil, err
	}
	if err := RequireExists(ctx, tx, eventID); err != nil {
		return uuid.Nil, nil, err
	}
	principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(
		ctx,
		tx,
		*sessionID,
	)
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return uuid.Nil, nil, authenticationRequired().WithCause(err)
	}
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("resolve Program Event collaboration principal: %w", err)
	}
	if principal == nil || !principal.Authenticated {
		return uuid.Nil, nil, authenticationRequired()
	}
	if principal.Banned {
		return uuid.Nil, nil, domainerr.Denied("account is banned")
	}
	if !principal.Onboarded {
		return uuid.Nil, nil, noPermission("edit")
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return uuid.Nil, nil, err
	}
	if !active {
		return uuid.Nil, nil, noPermission("edit")
	}
	subject, err := auth.NewAccountIdentitySubject(principal.IdentityID)
	if err != nil {
		return uuid.Nil, nil, authenticationRequired().WithCause(err)
	}
	var root struct {
		Status model.ProgramEventStatus `gorm:"column:status"`
	}
	if err := tx.WithContext(ctx).Table("program_event").Select("status").Where("id = ?", eventID).Take(&root).Error; err != nil {
		return uuid.Nil, nil, fmt.Errorf("load Program Event collaboration status: %w", err)
	}
	if err := requireProgramEventPermissionForSubject(
		ctx, spiceDB, eventID, programEventViewAction(root.Status), subject,
	); err != nil {
		return uuid.Nil, nil, err
	}
	if err := auth.LockAuthenticatedSessionForPrincipal(ctx, tx, *sessionID, principal); err != nil {
		if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
			return uuid.Nil, nil, authenticationRequired().WithCause(err)
		}
		return uuid.Nil, nil, fmt.Errorf("lock Program Event collaboration session: %w", err)
	}
	return documentID, principal, nil
}

func requireProgramEventContentContributors(
	ctx context.Context,
	tx *gorm.DB,
	checkpoints persistencecheckpoint.ContributorFence,
	eventID string,
	contributors []string,
) error {
	return checkpoints.RequireCurrentContributors(
		ctx,
		tx,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PROGRAM_EVENT,
		eventID,
		contributors,
	)
}

func normalizeProgramEventContentBlockError(err error) error {
	if err == nil || rejected(err, domainerr.Invalid, domainerr.NotFound, domainerr.AlreadyExists,
		domainerr.FailedPrecondition, domainerr.PermissionDenied, domainerr.Unauthenticated, domainerr.Unavailable) {
		return err
	}
	// A revision conflict keeps its cause, so a conflict found while storing
	// takes the same errors.Is/As branches (AI document conflict result,
	// translation source no longer current) as one found by the pre-check.
	var targetConflict *translation.TargetRevisionConflict
	if errors.As(err, &targetConflict) {
		return errors.Join(domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
			targetConflict.Error(), domainerr.CollaborationRevisionsOf(err),
		), err)
	}

	switch {
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return domainerr.New(domainerr.NotFound, "", "Program Event content document not found").WithCause(err)
	case errors.Is(err, contentblock.ErrStaleRevision):
		return errors.Join(domainerr.CollaborationConflict(
			intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
			"Program Event content revision changed; reload before saving", domainerr.CollaborationRevisionsOf(err),
		), err)
	case errors.Is(err, contentblock.ErrCrossDocument):
		return domainerr.InvalidField("blocks", "a Block belongs to another document").WithCause(err)
	case errors.Is(err, contentblock.ErrFileReference):
		return domainerr.InvalidField("blocks", err.Error())
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return domainerr.InvalidField("blocks", err.Error())
	default:
		return fmt.Errorf("program Event content document: %w", err)
	}
}
