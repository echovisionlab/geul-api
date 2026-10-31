package member

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

func (s *Service) loadMembers(ctx context.Context, ids []string) ([]model.Member, error) {
	return loadMembers(ctx, s.db, ids)
}

func loadMembers(ctx context.Context, db *gorm.DB, ids []string) ([]model.Member, error) {
	query := db.WithContext(ctx).Model(&model.Member{})
	if len(ids) != 0 {
		query = query.Where("id IN ?", ids)
	}
	var members []model.Member
	if err := query.Find(&members).Error; err != nil {
		return nil, err
	}
	return members, nil
}

func (s *Service) loadCurrentSessionMember(ctx context.Context, memberID string) (*model.Member, error) {
	var member model.Member
	if err := s.db.WithContext(ctx).
		Select("id", "account_identity_id", "nickname", "onboarded", "preferred_locale", "deleted_at").
		Where("id = ?", memberID).
		Take(&member).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("member", memberID)
		}
		return nil, err
	}
	return &member, nil
}

func (s *Service) loadAvatarAssets(ctx context.Context, memberIDs []string) (map[string]*commonv1.AssetRef, error) {
	return loadAvatarAssets(ctx, s.db, s.cdnDomain, memberIDs)
}

func loadAvatarAssets(ctx context.Context, db *gorm.DB, cdnDomain string, memberIDs []string) (map[string]*commonv1.AssetRef, error) {
	result := make(map[string]*commonv1.AssetRef, len(memberIDs))
	if len(memberIDs) == 0 {
		return result, nil
	}
	type avatarRow struct {
		MemberID string `gorm:"column:member_id"`
		model.PublicAsset
	}
	var rows []avatarRow
	if err := db.WithContext(ctx).Raw(`
		SELECT b.owner_id AS member_id, a.*
		FROM public_asset_binding AS b
		JOIN public_asset AS a ON a.id = b.asset_id
		WHERE b.owner_type = 'member'
		  AND b.binding_key = 'avatar'
		  AND b.owner_id IN ?
		  AND a.status = 'ready'
	`, memberIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	lifecycle := mediaasset.NewLifecycle(db, cdnDomain)
	for _, row := range rows {
		asset, err := lifecycle.AssetRef(row.PublicAsset)
		if err != nil {
			return nil, err
		}
		result[row.MemberID] = asset
	}
	return result, nil
}

func memberSummary(member model.Member, avatar *commonv1.AssetRef) *commonv1.MemberSummary {
	deleted := memberIsTombstone(member)
	if deleted {
		// A tombstone must never expose profile PII, even while asynchronous
		// cleanup of the former avatar binding is still pending.
		avatar = nil
	}
	return &commonv1.MemberSummary{Id: member.ID, Nickname: member.Nickname, AvatarAsset: avatar, Deleted: deleted}
}

func memberIsTombstone(member model.Member) bool {
	return member.DeletedAt != nil || member.AccountIdentityID == nil
}

func loadMemberSummaries(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	memberIDs []string,
) (map[string]*commonv1.MemberSummary, error) {
	result := make(map[string]*commonv1.MemberSummary, len(memberIDs))
	if len(memberIDs) == 0 {
		return result, nil
	}
	members, err := loadMembers(ctx, db, memberIDs)
	if err != nil {
		return nil, err
	}
	assets, err := loadAvatarAssets(ctx, db, cdnDomain, memberIDs)
	if err != nil {
		return nil, err
	}
	for i := range members {
		result[members[i].ID] = memberSummary(members[i], assets[members[i].ID])
	}
	return result, nil
}

func LoadSummaries(ctx context.Context, db *gorm.DB, cdnDomain string, memberIDs []string) (map[string]*commonv1.MemberSummary, error) {
	return loadMemberSummaries(ctx, db, cdnDomain, memberIDs)
}

func loadAuthorizationEligibleMemberSummary(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	memberID string,
) (*commonv1.MemberSummary, error) {
	if _, err := authorizationtarget.Require(ctx, db, memberID); err != nil {
		return nil, err
	}
	summaries, err := loadMemberSummaries(ctx, db, cdnDomain, []string{memberID})
	if err != nil {
		return nil, err
	}
	summary := summaries[memberID]
	if summary == nil || summary.Deleted {
		return nil, domainerr.ResourceNotFound("member", memberID)
	}
	return summary, nil
}

func LoadAuthorizationEligibleSummary(ctx context.Context, db *gorm.DB, cdnDomain, memberID string) (*commonv1.MemberSummary, error) {
	return loadAuthorizationEligibleMemberSummary(ctx, db, cdnDomain, memberID)
}

func memberProfile(member model.Member, avatar *commonv1.AssetRef) *managev1.MemberProfile {
	profile := &managev1.MemberProfile{
		Summary:   memberSummary(member, avatar),
		CreatedAt: timestamppb.New(member.CreatedAt),
	}
	if !memberIsTombstone(member) {
		profile.Bio = member.Bio
		profile.Website = member.Website
		profile.SocialLinks = member.SocialLinks
		profile.PreferredLocale = member.PreferredLocale
		if !member.UpdatedAt.IsZero() {
			profile.UpdatedAt = timestamppb.New(member.UpdatedAt)
		}
	}
	return profile
}

func hideExtendedMemberProfile(profile *managev1.MemberProfile) {
	if profile == nil {
		return
	}
	profile.Bio = nil
	profile.Website = nil
	profile.SocialLinks = nil
}

func (s *Service) validateSelfMemberProfileMutation(
	ctx context.Context,
	bio *string,
	website *string,
	socialLinks map[string]string,
) error {
	author, err := s.isGlobalAuthor(ctx)
	if err != nil {
		return err
	}
	if author {
		return nil
	}
	if bio != nil || website != nil || socialLinks != nil {
		return domainerr.Denied("extended profile fields require Author or Admin role")
	}
	return nil
}

func (s *Service) memberProfileByID(ctx context.Context, memberID string) (*model.Member, *managev1.MemberProfile, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return nil, nil, domainerr.InvalidField("member_id", "must be a canonical UUID")
	}
	members, err := s.loadMembers(ctx, []string{memberID})
	if err != nil {
		return nil, nil, err
	}
	if len(members) != 1 {
		return nil, nil, domainerr.ResourceNotFound("member", memberID)
	}
	assets, err := s.loadAvatarAssets(ctx, []string{memberID})
	if err != nil {
		return nil, nil, err
	}
	return &members[0], memberProfile(members[0], assets[memberID]), nil
}
