package member

import (
	"context"
	"errors"
	"maps"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

// GetSettings returns the authenticated Member's settings.
func (s *Service) GetSettings(ctx context.Context) (*managev1.GetMySettingsResponse, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	memberID := principal.MemberID.String()
	var member model.Member
	if err := s.db.WithContext(ctx).Where("id = ? AND deleted_at IS NULL AND account_identity_id IS NOT NULL", memberID).First(&member).Error; err != nil {
		return nil, err
	}
	subscription, err := newsletterSubscriptionState(ctx, s.db, principal.IdentityID.String())
	if err != nil {
		return nil, err
	}
	var consent model.UserCookieConsent
	consentErr := s.db.WithContext(ctx).Where("member_id = ?", memberID).Order("recorded_at DESC").First(&consent).Error
	if consentErr != nil && !errors.Is(consentErr, gorm.ErrRecordNotFound) {
		return nil, consentErr
	}
	account, err := s.accounts.SummaryForMember(ctx, s.db, s.spicedb, memberID)
	if err != nil {
		return nil, err
	}
	settings := &managev1.GetMySettingsResponse{PreferredLocale: member.PreferredLocale, NewsletterSubscription: subscription, CookieConsent: cookieConsentProto(nil), CanonicalEmail: account.CanonicalEmail}
	if consentErr == nil {
		settings.CookieConsent = cookieConsentProto(&consent)
	}
	return settings, nil
}

func normalizeMemberNickname(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > memberNicknameMaxLength {
		return "", domainerr.InvalidField("nickname", "must be between 1 and 100 characters")
	}
	return value, nil
}

// profileEdit is the requested profile change. A nil field stays unchanged.
type profileEdit struct {
	Nickname    *string
	Bio         *string
	Website     *string
	SocialLinks map[string]string
}

func (edit profileEdit) updates() (structured.Fields, error) {
	updates := structured.Fields{}
	if edit.Nickname != nil {
		value, err := normalizeMemberNickname(*edit.Nickname)
		if err != nil {
			return nil, err
		}
		updates["nickname"] = value
	}
	if edit.Bio != nil {
		updates["bio"] = nullableTrimmed(*edit.Bio)
	}
	if edit.Website != nil {
		updates["website"] = nullableTrimmed(*edit.Website)
	}
	if edit.SocialLinks != nil {
		for key, value := range edit.SocialLinks {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
				return nil, domainerr.InvalidField("social_links", "must not contain blank keys or values")
			}
		}
		updates["social_links"] = edit.SocialLinks
	}
	updates["updated_at"] = time.Now().UTC()
	return updates, nil
}

// failure classifies a profile update failure: a taken nickname and a
// missing Member are client outcomes, and the submitted values stay out of
// the failure record.
func (edit profileEdit) failure(err error, memberID string) error {
	if edit.Nickname != nil && dberrors.IsUniqueViolation(err) {
		nickname := strings.TrimSpace(*edit.Nickname)
		return domainerr.Duplicate("member", "nickname", nickname).WithCause(failurecause.WithPrivate(err, *edit.Nickname, nickname))
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainerr.ResourceNotFound("member", memberID)
	}
	return err
}

func nullableTrimmed(value string) structured.Value {
	if value = strings.TrimSpace(value); value == "" {
		return nil
	}
	return value
}

// profileChange is what a profile update changed, for its audit record.
type profileChange struct {
	fields   []string
	nickname string
}

func (change profileChange) audit(memberID string) mutation.Audit {
	if len(change.fields) == 0 {
		return mutation.Unchanged()
	}
	return mutation.RequestAudit(sharedtelemetry.AuditMemberUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewMemberProfileUpdatedAuditRecord(metadata, memberID, change.fields, change.nickname)
	})
}

// applyProfileUpdates writes updates to the Member row read by where, when
// they change it.
func applyProfileUpdates(tx *gorm.DB, memberID string, updates structured.Fields, where string, args ...any) (profileChange, error) {
	private := profileUpdatePrivateValues(updates)
	var current model.Member
	if err := tx.Where(where, args...).Take(&current).Error; err != nil {
		return profileChange{}, failurecause.WithPrivate(err, private...)
	}
	fields, nickname := memberProfileChangedFields(current, updates)
	if len(fields) == 0 {
		return profileChange{}, nil
	}
	if err := tx.Model(&model.Member{}).Where("id = ?::uuid", memberID).Updates(updates).Error; err != nil {
		return profileChange{}, failurecause.WithPrivate(err, private...)
	}
	return profileChange{fields: fields, nickname: nickname}, nil
}

// UpdateOwnProfileCommand edits the authenticated Member's own profile. A nil
// field stays unchanged; bio, website and social links require the Author
// role.
type UpdateOwnProfileCommand struct {
	Nickname    *string
	Bio         *string
	Website     *string
	SocialLinks map[string]string
}

// UpdateOwnProfile applies command and returns the Member summary.
func (s *Service) UpdateOwnProfile(ctx context.Context, command UpdateOwnProfileCommand) (*commonv1.MemberSummary, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.validateSelfMemberProfileMutation(ctx, command.Bio, command.Website, command.SocialLinks); err != nil {
		return nil, err
	}
	return s.updateOwnProfile(ctx, principal, profileEdit(command))
}

// updateOwnProfile applies edit to the principal's own profile after the
// role check of the extended fields.
func (s *Service) updateOwnProfile(ctx context.Context, principal *auth.UserInfo, edit profileEdit) (*commonv1.MemberSummary, error) {
	memberID := principal.MemberID.String()
	updates, err := edit.updates()
	if err != nil {
		return nil, err
	}
	target, err := authorizationtarget.Require(ctx, s.db, memberID)
	if err != nil {
		return nil, err
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.Spec[profileChange]{
		Subjects: []string{target.IdentityID},
		Access:   mutation.PrincipalFence(principal),
		Change: func(_ context.Context, tx *gorm.DB) (profileChange, error) {
			if err := recheckTarget(tx, memberID, target, authorizationtarget.ForMember); err != nil {
				return profileChange{}, err
			}
			return applyProfileUpdates(tx, memberID, updates, "id = ? AND deleted_at IS NULL AND account_identity_id IS NOT NULL AND onboarded = TRUE", memberID)
		},
		Audit: func(change profileChange) mutation.Audit { return change.audit(memberID) },
	}); err != nil {
		return nil, edit.failure(err, memberID)
	}
	_, profile, err := s.memberProfileByID(ctx, memberID)
	if err != nil {
		return nil, err
	}
	return profile.Summary, nil
}

func (s *Service) updateMemberProfileAsAdmin(ctx context.Context, command UpdateMemberProfileCommand) error {
	edit := profileEdit{Nickname: command.Nickname, Bio: command.Bio, Website: command.Website, SocialLinks: command.SocialLinks}
	updates, err := edit.updates()
	if err != nil {
		return err
	}
	memberID := command.MemberID
	target, err := authorizationtarget.RequireLinkedMember(ctx, s.db, memberID, false)
	if err != nil {
		return err
	}
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[profileChange]{
		Subjects: []string{target.IdentityID},
		Access:   freshAdmin(),
		Change: func(_ context.Context, tx *gorm.DB) (profileChange, error) {
			if err := recheckTarget(tx, memberID, target, linkedMember(false)); err != nil {
				return profileChange{}, err
			}
			return applyProfileUpdates(tx, memberID, updates, "id = ?::uuid AND account_identity_id = ?::uuid AND deleted_at IS NULL", memberID, target.IdentityID)
		},
		Audit: func(change profileChange) mutation.Audit { return change.audit(memberID) },
	})
	if err != nil {
		return edit.failure(err, memberID)
	}
	return nil
}

// recheckTarget re-reads the target Member under the mutation's Identity
// lock and requires the Identity resolved before the transaction.
func recheckTarget(
	tx *gorm.DB,
	memberID string,
	target authorizationtarget.Target,
	read func(*gorm.DB, string) (authorizationtarget.Target, error),
) error {
	current, err := read(tx, memberID)
	if errors.Is(err, authorizationtarget.ErrIneligible) {
		return domainerr.ResourceNotFound("member", memberID)
	}
	if err != nil {
		return err
	}
	if current.IdentityID != target.IdentityID {
		return domainerr.ResourceNotFound("member", memberID)
	}
	return nil
}

// linkedMember reads a Member with a live Identity link, onboarded or not.
func linkedMember(requireOnboarded bool) func(*gorm.DB, string) (authorizationtarget.Target, error) {
	return func(tx *gorm.DB, memberID string) (authorizationtarget.Target, error) {
		return authorizationtarget.LinkedMemberForMember(tx, memberID, requireOnboarded)
	}
}

// profileUpdatePrivateValues lists the submitted profile values, which a
// database error can echo, so the failure record redacts them.
func profileUpdatePrivateValues(updates structured.Fields) []string {
	var values []string
	for _, value := range updates {
		switch value := value.(type) {
		case string:
			values = append(values, value)
		case map[string]string:
			for _, entry := range value {
				values = append(values, entry)
			}
		}
	}
	return values
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

// CheckNicknameAvailabilityQuery asks whether another Member holds a nickname.
type CheckNicknameAvailabilityQuery struct {
	Nickname string
}

// CheckNicknameAvailability reports whether the authenticated Member may
// take query.Nickname.
func (s *Service) CheckNicknameAvailability(ctx context.Context, query CheckNicknameAvailabilityQuery) (bool, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return false, err
	}
	nickname, err := normalizeMemberNickname(query.Nickname)
	if err != nil {
		return false, err
	}
	var claimed bool
	if err := s.db.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM member
			WHERE nickname = ?
			  AND id <> ?::uuid
			  AND deleted_at IS NULL
		)
	`, nickname, principal.MemberID.String()).Scan(&claimed).Error; err != nil {
		return false, failurecause.WithPrivate(err, nickname)
	}
	return !claimed, nil
}
