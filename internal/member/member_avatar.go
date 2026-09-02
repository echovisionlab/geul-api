package member

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func (s *MemberService) setAvatar(ctx context.Context, memberID, fileID string, admin bool) (*commonv1.MemberSummary, error) {
	if _, err := uuidutil.ParseCanonical(fileID, "file_id"); err != nil {
		return nil, errs.InvalidArgument("file_id", "must be a canonical UUID")
	}
	target, err := authorizationtarget.RequireLinkedMember(ctx, s.db, memberID, !admin)
	if err != nil {
		return nil, err
	}
	var asset *commonv1.AssetRef
	if err := identitystate.WithMutation(ctx, s.db, target.IdentityID, func(mutationCtx context.Context, connection *gorm.DB) error {
		return connection.WithContext(mutationCtx).Transaction(func(tx *gorm.DB) error {
			current, err := authorizationtarget.LinkedMemberForMember(tx, memberID, !admin)
			if err != nil {
				if errors.Is(err, authorizationtarget.ErrIneligible) {
					return errs.NotFound("member", memberID)
				}
				return errs.Internal(err)
			}
			if current.IdentityID != target.IdentityID {
				return errs.NotFound("member", memberID)
			}
			query := tx.Model(&model.FileIngestBinding{}).Where("file_id = ? AND upload_type = ?", fileID, managev1.UploadType_UPLOAD_TYPE_USER_AVATAR.String())
			if !admin {
				query = query.Where("entity_id = ?", memberID)
			}
			var count int64
			if err := query.Count(&count).Error; err != nil {
				return errs.Internal(err)
			}
			if count != 1 {
				return errs.PermissionDenied("avatar source does not belong to this member")
			}
			promoter, ok := s.fileService.(UserAvatarAssetPromoter)
			if !ok {
				return errs.Internal(fmt.Errorf("file service does not support avatar promotion"))
			}
			promoted, err := promoter.PromoteUserAvatarAsset(mutationCtx, fileID)
			if err != nil {
				return err
			}
			binding, found, err := mediaasset.LoadPublicAssetBindingForUpdate(mutationCtx, tx, "member", memberID, "avatar")
			if err != nil {
				return errs.Internal(err)
			}
			asset = promoted
			if found && binding.AssetID == promoted.AssetId {
				return nil
			}
			if err := mediaasset.NewLifecycle(tx, s.cdnDomain).BindPublicAsset(mutationCtx, mediaasset.Binding{AssetID: promoted.AssetId, OwnerType: "member", OwnerID: memberID, BindingKey: "avatar", SourceFileID: &fileID}); err != nil {
				return err
			}
			return domainaudit.AppendRequest(
				mutationCtx,
				tx,
				s.auditWriter,
				sharedtelemetry.AuditMemberUpdated,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewMemberAvatarUpdatedAuditRecord(
						metadata,
						memberID,
						sharedtelemetry.AuditCollectionOperationAdded,
						promoted.AssetId,
					)
				},
			)
		})
	}); err != nil {
		return nil, errs.Wrap(err)
	}
	members, err := s.loadMembers(ctx, []string{memberID})
	if err != nil || len(members) != 1 {
		return nil, errs.NotFound("member", memberID)
	}
	return memberSummary(members[0], asset), nil
}

func (s *MemberService) deleteAvatar(ctx context.Context, memberID string, admin bool) (*commonv1.MemberSummary, error) {
	target, err := authorizationtarget.RequireLinkedMember(ctx, s.db, memberID, !admin)
	if err != nil {
		return nil, err
	}
	if err := identitystate.WithMutation(ctx, s.db, target.IdentityID, func(mutationCtx context.Context, connection *gorm.DB) error {
		return connection.WithContext(mutationCtx).Transaction(func(tx *gorm.DB) error {
			current, err := authorizationtarget.LinkedMemberForMember(tx, memberID, !admin)
			if err != nil {
				if errors.Is(err, authorizationtarget.ErrIneligible) {
					return errs.NotFound("member", memberID)
				}
				return errs.Internal(err)
			}
			if current.IdentityID != target.IdentityID {
				return errs.NotFound("member", memberID)
			}
			binding, found, err := mediaasset.LoadPublicAssetBindingForUpdate(mutationCtx, tx, "member", memberID, "avatar")
			if err != nil {
				return errs.Internal(err)
			}
			if !found {
				return nil
			}
			if err := mediaasset.NewLifecycle(tx, s.cdnDomain).ReleaseExactPublicAssetBindings(mutationCtx, "member", memberID, []string{"avatar"}); err != nil {
				return err
			}
			return domainaudit.AppendRequest(
				mutationCtx,
				tx,
				s.auditWriter,
				sharedtelemetry.AuditMemberUpdated,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewMemberAvatarUpdatedAuditRecord(
						metadata,
						memberID,
						sharedtelemetry.AuditCollectionOperationRemoved,
						binding.AssetID,
					)
				},
			)
		})
	}); err != nil {
		return nil, errs.Wrap(err)
	}
	members, err := s.loadMembers(ctx, []string{memberID})
	if err != nil || len(members) != 1 {
		return nil, errs.NotFound("member", memberID)
	}
	return memberSummary(members[0], nil), nil
}

func (s *MemberService) SetMyAvatar(ctx context.Context, req *connect.Request[managev1.SetMyAvatarRequest]) (*connect.Response[managev1.MemberSummaryMutationResponse], error) {
	p, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	summary, err := s.setAvatar(ctx, p.MemberID.String(), req.Msg.FileId, false)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.MemberSummaryMutationResponse{Member: summary}), nil
}
func (s *MemberService) DeleteMyAvatar(ctx context.Context, _ *connect.Request[managev1.DeleteMyAvatarRequest]) (*connect.Response[managev1.MemberSummaryMutationResponse], error) {
	p, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	summary, err := s.deleteAvatar(ctx, p.MemberID.String(), false)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.MemberSummaryMutationResponse{Member: summary}), nil
}
