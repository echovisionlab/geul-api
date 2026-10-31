package member

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

// avatarChange is the avatar binding after a mutation; assetID is the asset
// the audit record names, empty when nothing changed.
type avatarChange struct {
	asset   *commonv1.AssetRef
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
func (s *Service) setAvatar(ctx context.Context, memberID, fileID string, access mutation.Access, admin bool) (*commonv1.MemberSummary, error) {
	if _, err := uuidutil.ParseCanonical(fileID, "file_id"); err != nil {
		return nil, domainerr.InvalidField("file_id", "must be a canonical UUID")
	}
	target, err := authorizationtarget.RequireLinkedMember(ctx, s.db, memberID, !admin)
	if err != nil {
		return nil, err
	}
	change, err := mutation.Run(ctx, s.mutations, mutation.Spec[avatarChange]{
		Subjects: []string{target.IdentityID},
		Access:   access,
		Change: func(ctx context.Context, tx *gorm.DB) (avatarChange, error) {
			if err := recheckTarget(tx, memberID, target, linkedMember(!admin)); err != nil {
				return avatarChange{}, err
			}
			query := tx.Model(&model.FileIngestBinding{}).Where("file_id = ? AND upload_type = ?", fileID, managev1.UploadType_UPLOAD_TYPE_USER_AVATAR.String())
			if !admin {
				query = query.Where("entity_id = ?", memberID)
			}
			var count int64
			if err := query.Count(&count).Error; err != nil {
				return avatarChange{}, err
			}
			if count != 1 {
				return avatarChange{}, domainerr.Denied("avatar source does not belong to this member")
			}
			promoted, err := s.avatarFiles.PromoteUserAvatarAsset(ctx, fileID)
			if err != nil {
				return avatarChange{}, err
			}
			binding, found, err := mediaasset.LoadPublicAssetBindingForUpdate(ctx, tx, "member", memberID, "avatar")
			if err != nil {
				return avatarChange{}, err
			}
			if found && binding.AssetID == promoted.AssetId {
				return avatarChange{asset: promoted}, nil
			}
			if err := mediaasset.NewLifecycle(tx, s.cdnDomain).BindPublicAsset(ctx, mediaasset.Binding{AssetID: promoted.AssetId, OwnerType: "member", OwnerID: memberID, BindingKey: "avatar", SourceFileID: &fileID}); err != nil {
				return avatarChange{}, err
			}
			return avatarChange{asset: promoted, assetID: promoted.AssetId}, nil
		},
		Audit: func(change avatarChange) mutation.Audit {
			return change.audit(memberID, sharedtelemetry.AuditCollectionOperationAdded)
		},
	})
	if err != nil {
		return nil, err
	}
	return s.avatarMutationSummary(ctx, memberID, change.asset)
}

func (s *Service) deleteAvatar(ctx context.Context, memberID string, access mutation.Access, admin bool) (*commonv1.MemberSummary, error) {
	target, err := authorizationtarget.RequireLinkedMember(ctx, s.db, memberID, !admin)
	if err != nil {
		return nil, err
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.Spec[avatarChange]{
		Subjects: []string{target.IdentityID},
		Access:   access,
		Change: func(ctx context.Context, tx *gorm.DB) (avatarChange, error) {
			if err := recheckTarget(tx, memberID, target, linkedMember(!admin)); err != nil {
				return avatarChange{}, err
			}
			binding, found, err := mediaasset.LoadPublicAssetBindingForUpdate(ctx, tx, "member", memberID, "avatar")
			if err != nil || !found {
				return avatarChange{}, err
			}
			if err := mediaasset.NewLifecycle(tx, s.cdnDomain).ReleaseExactPublicAssetBindings(ctx, "member", memberID, []string{"avatar"}); err != nil {
				return avatarChange{}, err
			}
			return avatarChange{assetID: binding.AssetID}, nil
		},
		Audit: func(change avatarChange) mutation.Audit {
			return change.audit(memberID, sharedtelemetry.AuditCollectionOperationRemoved)
		},
	}); err != nil {
		return nil, err
	}
	return s.avatarMutationSummary(ctx, memberID, nil)
}

func (s *Service) avatarMutationSummary(ctx context.Context, memberID string, asset *commonv1.AssetRef) (*commonv1.MemberSummary, error) {
	members, err := s.loadMembers(ctx, []string{memberID})
	if err != nil {
		return nil, fmt.Errorf("load member after avatar mutation: %w", err)
	}
	if len(members) != 1 {
		return nil, domainerr.ResourceNotFound("member", memberID)
	}
	return memberSummary(members[0], asset), nil
}

// SetOwnAvatarCommand replaces the authenticated Member's avatar with their
// own uploaded avatar source File.
type SetOwnAvatarCommand struct {
	FileID string
}

// SetOwnAvatar applies command and returns the Member summary.
func (s *Service) SetOwnAvatar(ctx context.Context, command SetOwnAvatarCommand) (*commonv1.MemberSummary, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	return s.setAvatar(ctx, principal.MemberID.String(), command.FileID, mutation.PrincipalFence(principal), false)
}

// DeleteOwnAvatar removes the authenticated Member's avatar.
func (s *Service) DeleteOwnAvatar(ctx context.Context) (*commonv1.MemberSummary, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	return s.deleteAvatar(ctx, principal.MemberID.String(), mutation.PrincipalFence(principal), false)
}
