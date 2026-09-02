package worker

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (h *Handlers) handleUserDeleteIdentityMessage(ctx context.Context, msg mq.Message) error {
	var command managev1.UserDeleteIdentityCommand
	if err := proto.Unmarshal(msg.Body, &command); err != nil {
		return terminalQueueContractError("invalid_user_delete_identity", err)
	}
	if err := validateUserDeletionCommandMemberID(command.GetMemberId()); err != nil {
		return terminalQueueContractError("invalid_user_delete_identity", err)
	}
	if err := validateUserDeletionCommandIdentityID(command.GetIdentityId()); err != nil {
		return terminalQueueContractError("invalid_user_delete_identity", err)
	}
	if err := validateUserDeletionCommandMode(command.GetMode()); err != nil {
		return terminalQueueContractError("invalid_user_delete_identity", err)
	}
	if command.GetMemberId() == command.GetIdentityId() {
		return terminalQueueContractError(
			"invalid_user_delete_identity",
			fmt.Errorf("member_id and identity_id must be distinct"),
		)
	}
	if err := requireStableDeliveryMessageID(
		msg,
		"user-delete-identity:"+command.GetMemberId(),
	); err != nil {
		return err
	}
	publisher, ok := h.publisher.(account.EmailCommandPublisher)
	if !ok {
		return fmt.Errorf("user deletion fanout publisher is not configured")
	}
	return account.ProcessUserDeleteIdentityAudited(
		ctx, h.db, h.kratosClient, h.spicedbClient, h.memberDeletion, publisher, h.auditWriter, &command,
	)
}

func validateUserDeletionCommandMode(mode managev1.UserDeleteIdentityMode) error {
	switch mode {
	case managev1.UserDeleteIdentityMode_TOMBSTONE,
		managev1.UserDeleteIdentityMode_UNONBOARDED_HARD_DELETE:
		return nil
	default:
		return fmt.Errorf("user deletion identity mode must be explicit")
	}
}

func (h *Handlers) handleUserDeleteAvatarMessage(ctx context.Context, msg mq.Message) error {
	var command managev1.UserDeleteAvatarCommand
	if err := proto.Unmarshal(msg.Body, &command); err != nil {
		return terminalQueueContractError("invalid_user_delete_avatar", err)
	}
	if err := validateUserDeletionCommandMemberID(command.GetMemberId()); err != nil {
		return terminalQueueContractError("invalid_user_delete_avatar", err)
	}
	if err := requireStableDeliveryMessageID(
		msg,
		"user-delete-avatar:"+command.GetMemberId(),
	); err != nil {
		return err
	}
	if assetID := command.GetAvatarAssetId(); assetID != "" {
		if _, err := uuidutil.ParseCanonical(assetID, "avatar_asset_id"); err != nil {
			return terminalQueueContractError(
				"invalid_user_delete_avatar",
				fmt.Errorf("invalid user avatar deletion command: %w", err),
			)
		}
	}
	return account.ProcessUserDeleteAvatar(ctx, h.db, h.memberDeletion, command.GetMemberId(), command.GetAvatarAssetId())
}

func validateUserDeletionCommandMemberID(memberID string) error {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return err
	}
	return nil
}

func validateUserDeletionCommandIdentityID(identityID string) error {
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return err
	}
	return nil
}

// handleEmailSendMessage handles messages from email.send queue
