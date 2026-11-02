package account

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// DispatchScheduledUserDeletion enqueues the deletion commands of a due
// request and removes the request, in one transaction that holds the
// request's Identity fence. A request that is gone or not due is left alone.
// The step writes no audit record and answers to no principal, so it runs
// in its own transaction rather than the mutation pipeline.
func DispatchScheduledUserDeletion(
	ctx context.Context,
	units Units,
	spicedb GlobalRoles,
	requestID string,
	now time.Time,
) error {
	if units == nil || spicedb == nil {
		return fmt.Errorf("scheduled user deletion dependencies are required")
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return fmt.Errorf("scheduled user deletion requires request id")
	}
	now = now.UTC()
	var (
		memberID, identityID string
		found                bool
	)
	if err := units.Read(ctx, func(store Store) error {
		var err error
		memberID, identityID, found, err = store.DeletionRequestPair(ctx, requestID)
		return err
	}); err != nil || !found {
		return err
	}
	if err := validateDeletionIDPair(memberID, identityID); err != nil {
		return err
	}

	return units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		store := tx.Store()
		if err := store.LockIdentity(ctx, identityID); err != nil {
			return err
		}
		request, found, err := store.LockDueDeletionRequest(ctx, requestID, memberID, identityID, now)
		if err != nil || !found {
			return err
		}
		return dispatchLockedUserDeletion(ctx, store, spicedb, &request)
	})
}

// dispatchLockedUserDeletion enqueues the deletion commands of a due request
// whose Identity fence the store's transaction holds, and removes the
// request in the same transaction.
func dispatchLockedUserDeletion(
	ctx context.Context,
	store Store,
	spicedb GlobalRoles,
	request *DeletionRequest,
) error {
	if err := validateLastActiveAdminDeletion(ctx, store, spicedb, request.MemberID, request.IdentityID); err != nil {
		return err
	}
	identityState, err := store.LockedIdentityState(ctx, request.IdentityID)
	if err != nil {
		return err
	}
	if identityState == "" {
		return fmt.Errorf("scheduled deletion identity is missing before dispatch")
	}
	if identityState == auth.KratosStateActive {
		return ErrScheduledDeletionIdentityActive
	}
	command, err := buildUserDeleteIdentityCommand(ctx, store, request)
	if err != nil {
		return err
	}
	if err := store.EnqueueUserDeleteIdentity(ctx, command); err != nil {
		return err
	}
	if err := store.EnqueueUserDeleteAvatar(ctx, buildUserDeleteAvatarCommand(command)); err != nil {
		return err
	}
	return store.DeleteDeletionRequest(ctx, request.ID)
}

func buildUserDeleteAvatarCommand(command DeleteIdentityCommand) DeleteAvatarCommand {
	avatar := DeleteAvatarCommand{MemberID: command.MemberID}
	if command.AvatarAssetID != nil {
		if assetID := strings.TrimSpace(*command.AvatarAssetID); assetID != "" {
			avatar.AvatarAssetID = &assetID
		}
	}
	return avatar
}

func buildUserDeleteIdentityCommand(ctx context.Context, store Store, request *DeletionRequest) (DeleteIdentityCommand, error) {
	if request == nil {
		return DeleteIdentityCommand{}, fmt.Errorf("user deletion request is required")
	}
	if err := validateDeletionIDPair(request.MemberID, request.IdentityID); err != nil {
		return DeleteIdentityCommand{}, err
	}
	emailSnapshot := normalizeAccountEmail(ptrStringValue(request.NotificationEmail))
	nameSnapshot := strings.TrimSpace(ptrStringValue(request.NotificationName))
	if emailSnapshot == "" || nameSnapshot == "" || request.NotificationEmailVerifiedAt == nil {
		return DeleteIdentityCommand{}, fmt.Errorf("verified deletion notification snapshot is required")
	}
	command := DeleteIdentityCommand{
		Mode:              DeleteIdentityTombstone,
		MemberID:          request.MemberID,
		IdentityID:        request.IdentityID,
		NotificationEmail: &emailSnapshot,
		NotificationName:  &nameSnapshot,
	}
	assetID, err := store.MemberAvatarAssetID(ctx, request.MemberID)
	if err != nil {
		return DeleteIdentityCommand{}, err
	}
	if assetID != "" {
		command.AvatarAssetID = &assetID
	}
	if request.NotificationLocale != nil {
		if value := localization.NormalizeSupportedLocale(*request.NotificationLocale); value != nil {
			command.NotificationLocale = value
		}
	}
	return command, nil
}
