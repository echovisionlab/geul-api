package programevent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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
		return uuid.Nil, errs.Internal(errors.New("program Event content document database is required"))
	}
	query := db.WithContext(ctx).
		Table("program_event").
		Select("content_document_id").
		Where("id = ?", eventID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var root programEventContentDocumentRoot
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, errs.NotFound("program event", eventID)
		}
		return uuid.Nil, errs.Internal(err)
	}
	if !root.ContentDocumentID.Valid || root.ContentDocumentID.String == "" {
		return uuid.Nil, errs.FailedPrecondition("Program Event content document is not initialized")
	}
	documentID, err := uuid.Parse(root.ContentDocumentID.String)
	if err != nil {
		return uuid.Nil, errs.Internal(fmt.Errorf("invalid Program Event content_document_id: %w", err))
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
			Clauses(clause.Locking{Strength: "UPDATE"})
		var root programEventContentDocumentRoot
		if err := query.Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return contentblock.DomainContext{}, errs.NotFound("program event", eventID)
			}
			return contentblock.DomainContext{}, errs.Internal(err)
		}
		if !root.ContentDocumentID.Valid || root.ContentDocumentID.String != documentID.String() {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Program Event content document ownership changed; reload before saving")
		}
		if authorize == nil {
			return contentblock.DomainContext{}, errs.Internal(errors.New("program Event content document authorization is required"))
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
	principalMessage *intrav1.CollaborationPrincipal,
) (uuid.UUID, *auth.UserInfo, error) {
	if principalMessage == nil {
		return uuid.Nil, nil, errs.AuthenticationRequired()
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
		principalMessage.GetSessionId(),
	)
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return uuid.Nil, nil, errs.AuthenticationRequired()
	}
	if err != nil {
		return uuid.Nil, nil, errs.Internal(fmt.Errorf("resolve Program Event collaboration principal: %w", err))
	}
	if principal == nil || !principal.Authenticated {
		return uuid.Nil, nil, errs.AuthenticationRequired()
	}
	if principal.Banned {
		return uuid.Nil, nil, errs.AccountBanned()
	}
	if !principal.Onboarded {
		return uuid.Nil, nil, errs.NoPermission("edit", "program event")
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return uuid.Nil, nil, errs.Internal(err)
	}
	if !active {
		return uuid.Nil, nil, errs.NoPermission("edit", "program event")
	}
	subject, err := auth.NewAccountIdentitySubject(principal.IdentityID)
	if err != nil {
		return uuid.Nil, nil, errs.AuthenticationRequired()
	}
	var root struct {
		Status string `gorm:"column:status"`
	}
	if err := tx.WithContext(ctx).Table("program_event").Select("status").Where("id = ?", eventID).Take(&root).Error; err != nil {
		return uuid.Nil, nil, errs.Internal(fmt.Errorf("load Program Event collaboration status: %w", err))
	}
	if err := requireProgramEventPermissionForSubject(
		ctx, spiceDB, eventID, programEventViewAction(root.Status), subject,
	); err != nil {
		return uuid.Nil, nil, err
	}
	if err := auth.LockAuthenticatedSessionForPrincipal(ctx, tx, principalMessage.GetSessionId(), principal); err != nil {
		if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
			return uuid.Nil, nil, errs.AuthenticationRequired()
		}
		return uuid.Nil, nil, errs.Internal(fmt.Errorf("lock Program Event collaboration session: %w", err))
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
	if err == nil || connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	switch {
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return errs.NotFoundMsg("Program Event content document not found")
	case errors.Is(err, contentblock.ErrStaleRevision):
		return errs.FailedPrecondition("Program Event content revision changed; reload before saving")
	case errors.Is(err, contentblock.ErrCrossDocument):
		return errs.InvalidArgument("blocks", "a Block belongs to another document")
	case errors.Is(err, contentblock.ErrFileReference):
		return errs.InvalidArgument("blocks", err.Error())
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return errs.InvalidArgument("blocks", err.Error())
	default:
		return errs.Internal(fmt.Errorf("program Event content document: %w", err))
	}
}
