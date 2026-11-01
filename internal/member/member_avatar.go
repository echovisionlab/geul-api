package member

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// avatarChange is the avatar binding after a mutation; assetID is the asset
// the audit record names, empty when nothing changed.
type avatarChange struct {
	asset   *Asset
	assetID string
}

func (change avatarChange) audit(memberID string, operation sharedtelemetry.AuditCollectionOperation) mutation.Audit {
	if change.assetID == "" {
		return mutation.Unchanged()
	}
	return mutation.RequestAudit(sharedtelemetry.AuditMemberUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewMemberAvatarUpdatedAuditRecord(metadata, memberID, operation, change.assetID)
	})
}

// setAvatar binds the public asset promoted from fileID as the Member's
// avatar. An administrator may use any avatar upload; a Member only their own.
func (s *Service) setAvatar(ctx context.Context, memberID, fileID string, access mutation.Access, admin bool) (Summary, error) {
	if _, err := uuidutil.ParseCanonical(fileID, "file_id"); err != nil {
		return Summary{}, domainerr.InvalidField("file_id", "must be a canonical UUID")
	}
	target, err := s.requireLinkedTarget(ctx, memberID, !admin)
	if err != nil {
		return Summary{}, err
	}
	change, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[avatarChange, Store]{
		Subjects: []string{target.IdentityID},
		Access:   access,
		Change: func(ctx context.Context, store Store) (avatarChange, error) {
			if err := recheckTarget(ctx, store, memberID, target, linkedMember(!admin)); err != nil {
				return avatarChange{}, err
			}
			owner := memberID
			if admin {
				owner = ""
			}
			owned, err := store.AvatarSourceOwned(ctx, fileID, owner)
			if err != nil {
				return avatarChange{}, err
			}
			if !owned {
				return avatarChange{}, domainerr.Denied("avatar source does not belong to this member")
			}
			promoted, err := s.avatarFiles.PromoteUserAvatarAsset(ctx, fileID)
			if err != nil {
				return avatarChange{}, err
			}
			current, found, err := store.LockAvatarBinding(ctx, memberID)
			if err != nil {
				return avatarChange{}, err
			}
			if found && current == promoted.ID {
				return avatarChange{asset: promoted}, nil
			}
			if err := store.BindAvatar(ctx, memberID, promoted.ID, fileID); err != nil {
				return avatarChange{}, err
			}
			return avatarChange{asset: promoted, assetID: promoted.ID}, nil
		},
		Audit: func(change avatarChange) mutation.Audit {
			return change.audit(memberID, sharedtelemetry.AuditCollectionOperationAdded)
		},
	})
	if err != nil {
		return Summary{}, err
	}
	return s.avatarMutationSummary(ctx, memberID, change.asset)
}

func (s *Service) deleteAvatar(ctx context.Context, memberID string, access mutation.Access, admin bool) (Summary, error) {
	target, err := s.requireLinkedTarget(ctx, memberID, !admin)
	if err != nil {
		return Summary{}, err
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[avatarChange, Store]{
		Subjects: []string{target.IdentityID},
		Access:   access,
		Change: func(ctx context.Context, store Store) (avatarChange, error) {
			if err := recheckTarget(ctx, store, memberID, target, linkedMember(!admin)); err != nil {
				return avatarChange{}, err
			}
			assetID, found, err := store.LockAvatarBinding(ctx, memberID)
			if err != nil || !found {
				return avatarChange{}, err
			}
			if err := store.ReleaseAvatar(ctx, memberID); err != nil {
				return avatarChange{}, err
			}
			return avatarChange{assetID: assetID}, nil
		},
		Audit: func(change avatarChange) mutation.Audit {
			return change.audit(memberID, sharedtelemetry.AuditCollectionOperationRemoved)
		},
	}); err != nil {
		return Summary{}, err
	}
	return s.avatarMutationSummary(ctx, memberID, nil)
}

func (s *Service) avatarMutationSummary(ctx context.Context, memberID string, asset *Asset) (Summary, error) {
	var members []Member
	if err := s.read(ctx, func(store Store) error {
		var err error
		members, err = store.Members(ctx, []string{memberID})
		return err
	}); err != nil {
		return Summary{}, fmt.Errorf("load member after avatar mutation: %w", err)
	}
	if len(members) != 1 {
		return Summary{}, domainerr.ResourceNotFound("member", memberID)
	}
	return memberSummary(members[0], asset), nil
}

// SetOwnAvatarCommand replaces the authenticated Member's avatar with their
// own uploaded avatar source File.
type SetOwnAvatarCommand struct {
	FileID string
}

// SetOwnAvatar applies command and returns the Member summary.
func (s *Service) SetOwnAvatar(ctx context.Context, command SetOwnAvatarCommand) (Summary, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return Summary{}, err
	}
	return s.setAvatar(ctx, principal.MemberID.String(), command.FileID, mutation.PrincipalFence(principal), false)
}

// DeleteOwnAvatar removes the authenticated Member's avatar.
func (s *Service) DeleteOwnAvatar(ctx context.Context) (Summary, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return Summary{}, err
	}
	return s.deleteAvatar(ctx, principal.MemberID.String(), mutation.PrincipalFence(principal), false)
}
