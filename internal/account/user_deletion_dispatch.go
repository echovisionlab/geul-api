package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func DispatchScheduledUserDeletion(
	ctx context.Context,
	db *gorm.DB,
	publisher UserDeletionDispatchPublisher,
	spicedb *auth.SpiceDBClient,
	members MemberDeletionLifecycle,
	requestID string,
	now time.Time,
) error {
	if db == nil || publisher == nil || spicedb == nil || members == nil {
		return fmt.Errorf("scheduled user deletion dependencies are required")
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return fmt.Errorf("scheduled user deletion requires request id")
	}
	now = now.UTC()
	var candidate model.UserDeletionRequest
	if err := db.WithContext(ctx).
		Select("member_id", "identity_id").
		Where("id = ?", requestID).
		Take(&candidate).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if err := validateDeletionIDPair(candidate.MemberID, candidate.IdentityID); err != nil {
		return err
	}

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.Lock(tx, candidate.IdentityID); err != nil {
			return err
		}
		var request model.UserDeletionRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND member_id = ?::uuid AND identity_id = ?::uuid AND lifecycle_state IN ? AND scheduled_at IS NOT NULL AND scheduled_at <= ?", requestID, candidate.MemberID, candidate.IdentityID, accountLifecycleDeletionPendingStates, now).
			Take(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := ValidateLastActiveAdminDeletionWithAuthorization(ctx, tx, request.MemberID, request.IdentityID, spicedb); err != nil {
			return err
		}
		var identityState string
		if err := tx.Raw("SELECT state FROM kratos.identities WHERE id = ?::uuid FOR UPDATE", request.IdentityID).
			Scan(&identityState).Error; err != nil {
			return err
		}
		if identityState == "" {
			return fmt.Errorf("scheduled deletion identity is missing before dispatch")
		}
		if identityState == auth.KratosStateActive {
			return ErrScheduledDeletionIdentityActive
		}
		command, err := buildUserDeleteIdentityCommand(ctx, tx, members, &request)
		if err != nil {
			return err
		}
		avatarCommand := buildUserDeleteAvatarCommand(command)
		executor, ok := tx.Statement.ConnPool.(eventpkg.DBTX)
		if !ok {
			return fmt.Errorf("database transaction does not expose a PGMQ executor")
		}
		transactionalPublisher, ok := publisher.(transactionalUserDeletionDispatchPublisher)
		if !ok {
			return fmt.Errorf("scheduled user deletion publisher does not support transactional dispatch")
		}
		if err := transactionalPublisher.PublishUserDeleteIdentityWithExecutor(ctx, executor, command); err != nil {
			return err
		}
		if err := transactionalPublisher.PublishUserDeleteAvatarWithExecutor(ctx, executor, avatarCommand); err != nil {
			return err
		}
		return tx.Delete(&request).Error
	})
}

func buildUserDeleteAvatarCommand(command *managev1.UserDeleteIdentityCommand) *managev1.UserDeleteAvatarCommand {
	avatar := &managev1.UserDeleteAvatarCommand{MemberId: command.GetMemberId()}
	if assetID := strings.TrimSpace(command.GetAvatarAssetId()); assetID != "" {
		avatar.AvatarAssetId = &assetID
	}
	return avatar
}

func buildUserDeleteIdentityCommand(
	ctx context.Context, db *gorm.DB, members MemberDeletionLifecycle, request *model.UserDeletionRequest,
) (*managev1.UserDeleteIdentityCommand, error) {
	if request == nil {
		return nil, fmt.Errorf("user deletion request is required")
	}
	if err := validateDeletionIDPair(request.MemberID, request.IdentityID); err != nil {
		return nil, err
	}
	emailSnapshot := normalizeAccountEmail(ptrStringValue(request.NotificationEmail))
	nameSnapshot := strings.TrimSpace(ptrStringValue(request.NotificationName))
	if emailSnapshot == "" || nameSnapshot == "" || request.NotificationEmailVerifiedAt == nil {
		return nil, fmt.Errorf("verified deletion notification snapshot is required")
	}
	command := &managev1.UserDeleteIdentityCommand{
		Mode:              managev1.UserDeleteIdentityMode_TOMBSTONE,
		MemberId:          request.MemberID,
		IdentityId:        request.IdentityID,
		NotificationEmail: &emailSnapshot,
		NotificationName:  &nameSnapshot,
	}
	assetID, err := members.AvatarAssetID(ctx, db, request.MemberID)
	if err != nil {
		return nil, err
	}
	if assetID != "" {
		command.AvatarAssetId = &assetID
	}
	if request.NotificationLocale != nil {
		if value := localization.NormalizeSupportedLocale(*request.NotificationLocale); value != nil {
			command.NotificationLocale = value
		}
	}
	return command, nil
}
