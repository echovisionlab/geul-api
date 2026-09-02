package member

import (
	"context"
	"errors"
	"maps"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func (s *MemberService) GetMySettings(
	ctx context.Context,
	_ *connect.Request[managev1.GetMySettingsRequest],
) (*connect.Response[managev1.GetMySettingsResponse], error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	memberID := principal.MemberID.String()
	var member model.Member
	if err := s.db.WithContext(ctx).Where("id = ? AND deleted_at IS NULL AND account_identity_id IS NOT NULL", memberID).First(&member).Error; err != nil {
		return nil, errs.Internal(err)
	}
	subscription, err := newsletterSubscriptionState(ctx, s.db, principal.IdentityID.String())
	if err != nil {
		return nil, errs.Internal(err)
	}
	var consent model.UserCookieConsent
	consentErr := s.db.WithContext(ctx).Where("member_id = ?", memberID).Order("recorded_at DESC").First(&consent).Error
	if consentErr != nil && consentErr != gorm.ErrRecordNotFound {
		return nil, errs.Internal(consentErr)
	}
	account, err := s.accountSummary(ctx, memberID)
	if err != nil {
		return nil, err
	}
	response := &managev1.GetMySettingsResponse{PreferredLocale: member.PreferredLocale, NewsletterSubscription: subscription, CookieConsent: cookieConsentProto(nil), CanonicalEmail: account.CanonicalEmail}
	if consentErr == nil {
		response.CookieConsent = cookieConsentProto(&consent)
	}
	return connect.NewResponse(response), nil
}

func normalizeMemberNickname(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errs.InvalidArgument("nickname", "must be between 1 and 100 characters")
	}
	if utf8.RuneCountInString(value) > memberNicknameMaxLength {
		return "", errs.InvalidArgument("nickname", "must be between 1 and 100 characters")
	}
	return value, nil
}

func validateMemberProfileMutation(nickname *string, bio *string, website *string, links map[string]string) (structured.Fields, error) {
	updates := structured.Fields{}
	if nickname != nil {
		value, err := normalizeMemberNickname(*nickname)
		if err != nil {
			return nil, err
		}
		updates["nickname"] = value
	}
	if bio != nil {
		updates["bio"] = nullableTrimmed(*bio)
	}
	if website != nil {
		updates["website"] = nullableTrimmed(*website)
	}
	if links != nil {
		for key, value := range links {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
				return nil, errs.InvalidArgument("social_links", "must not contain blank keys or values")
			}
		}
		updates["social_links"] = links
	}
	updates["updated_at"] = time.Now().UTC()
	return updates, nil
}

func nullableTrimmed(value string) structured.Value {
	if value = strings.TrimSpace(value); value == "" {
		return nil
	}
	return value
}

func (s *MemberService) updateProfile(
	ctx context.Context,
	memberID string,
	nickname, bio, website *string,
	links map[string]string,
) (*managev1.MemberProfile, error) {
	updates, err := validateMemberProfileMutation(nickname, bio, website, links)
	if err != nil {
		return nil, err
	}
	if err := authorizationtarget.WithMutation(ctx, s.db, memberID, func(mutationCtx context.Context, connection *gorm.DB) error {
		return connection.WithContext(mutationCtx).Transaction(func(tx *gorm.DB) error {
			var current model.Member
			if err := tx.Where("id = ? AND deleted_at IS NULL AND account_identity_id IS NOT NULL AND onboarded = TRUE", memberID).Take(&current).Error; err != nil {
				return err
			}
			changedFields, auditNickname := memberProfileChangedFields(current, updates)
			if len(changedFields) == 0 {
				return nil
			}
			if err := tx.Model(&model.Member{}).Where("id = ?", memberID).Updates(updates).Error; err != nil {
				return err
			}
			return domainaudit.AppendRequest(
				mutationCtx,
				tx,
				s.auditWriter,
				sharedtelemetry.AuditMemberUpdated,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewMemberProfileUpdatedAuditRecord(metadata, memberID, changedFields, auditNickname)
				},
			)
		})
	}); err != nil {
		if nickname != nil && dberrors.IsUniqueViolation(err) {
			return nil, errs.AlreadyExists("member", "nickname", strings.TrimSpace(*nickname))
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("member", memberID)
		}
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	_, profile, err := s.memberProfileByID(ctx, memberID)
	return profile, err
}

func (s *MemberService) updateMemberProfileAsAdmin(
	ctx context.Context,
	memberID string,
	nickname, bio, website *string,
	links map[string]string,
) (*managev1.MemberProfile, error) {
	updates, err := validateMemberProfileMutation(nickname, bio, website, links)
	if err != nil {
		return nil, err
	}
	target, err := authorizationtarget.RequireLinkedMember(ctx, s.db, memberID, false)
	if err != nil {
		return nil, err
	}
	err = identitystate.WithMutation(ctx, s.db, target.IdentityID, func(mutationCtx context.Context, connection *gorm.DB) error {
		return connection.WithContext(mutationCtx).Transaction(func(tx *gorm.DB) error {
			current, err := authorizationtarget.LinkedMemberForMember(tx, memberID, false)
			if err != nil {
				if errors.Is(err, authorizationtarget.ErrIneligible) {
					return errs.NotFound("member", memberID)
				}
				return errs.Internal(err)
			}
			if current.IdentityID != target.IdentityID {
				return errs.NotFound("member", memberID)
			}
			var currentMember model.Member
			if err := tx.Where("id = ?::uuid AND account_identity_id = ?::uuid AND deleted_at IS NULL", memberID, target.IdentityID).Take(&currentMember).Error; err != nil {
				return err
			}
			changedFields, auditNickname := memberProfileChangedFields(currentMember, updates)
			if len(changedFields) == 0 {
				return nil
			}
			if err := tx.Model(&model.Member{}).Where("id = ?::uuid", memberID).Updates(updates).Error; err != nil {
				return err
			}
			return domainaudit.AppendRequest(
				mutationCtx,
				tx,
				s.auditWriter,
				sharedtelemetry.AuditMemberUpdated,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewMemberProfileUpdatedAuditRecord(metadata, memberID, changedFields, auditNickname)
				},
			)
		})
	})
	if err != nil {
		if nickname != nil && dberrors.IsUniqueViolation(err) {
			return nil, errs.AlreadyExists("member", "nickname", strings.TrimSpace(*nickname))
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("member", memberID)
		}
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	_, profile, err := s.memberProfileByID(ctx, memberID)
	return profile, err
}

func memberProfileChangedFields(current model.Member, updates structured.Fields) ([]string, string) {
	changedFields := make([]string, 0, 4)
	auditNickname := ""
	if nickname, ok := updates["nickname"].(string); ok && current.Nickname != nickname {
		changedFields = append(changedFields, "nickname")
		auditNickname = nickname
	}
	if desired, ok := updates["bio"]; ok && !sameNullableMemberProfileValue(current.Bio, desired) {
		changedFields = append(changedFields, "bio")
	}
	if desired, ok := updates["website"]; ok && !sameNullableMemberProfileValue(current.Website, desired) {
		changedFields = append(changedFields, "website")
	}
	if socialLinks, ok := updates["social_links"].(map[string]string); ok && !maps.Equal(current.SocialLinks, socialLinks) {
		changedFields = append(changedFields, "social_links")
	}
	sort.Strings(changedFields)
	return changedFields, auditNickname
}

func sameNullableMemberProfileValue(current *string, desired structured.Value) bool {
	if desired == nil {
		return current == nil || *current == ""
	}
	value, ok := desired.(string)
	return ok && current != nil && *current == value
}

func (s *MemberService) UpdateMyProfile(
	ctx context.Context,
	req *connect.Request[managev1.UpdateMyProfileRequest],
) (*connect.Response[managev1.UpdateMyProfileResponse], error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.validateSelfMemberProfileMutation(ctx, req.Msg.Bio, req.Msg.Website, req.Msg.SocialLinks); err != nil {
		return nil, err
	}
	profile, err := s.updateProfile(ctx, principal.MemberID.String(), req.Msg.Nickname, req.Msg.Bio, req.Msg.Website, req.Msg.SocialLinks)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.UpdateMyProfileResponse{Member: profile.Summary}), nil
}

func (s *MemberService) CheckNicknameAvailability(
	ctx context.Context,
	req *connect.Request[managev1.CheckNicknameAvailabilityRequest],
) (*connect.Response[managev1.CheckNicknameAvailabilityResponse], error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	nickname, err := normalizeMemberNickname(req.Msg.Nickname)
	if err != nil {
		return nil, err
	}
	var claimed bool
	if err := s.db.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM member
			WHERE nickname = ?
			  AND id <> ?::uuid
		)
	`, nickname, principal.MemberID.String()).Scan(&claimed).Error; err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.CheckNicknameAvailabilityResponse{Available: !claimed}), nil
}
