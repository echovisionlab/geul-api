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
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/mutation"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Settings are the authenticated Member's own settings.
type Settings struct {
	PreferredLocale *string
	Newsletter      NewsletterState
	CookieConsent   *CookieConsent
	CanonicalEmail  *CanonicalEmail
}

// GetSettings returns the authenticated Member's settings.
func (s *Service) GetSettings(ctx context.Context) (Settings, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return Settings{}, err
	}
	memberID := principal.MemberID.String()
	var settings Settings
	if err := s.read(ctx, func(store Store) error {
		member, err := store.SettingsMember(ctx, memberID)
		if err != nil {
			return err
		}
		settings.PreferredLocale = member.PreferredLocale
		if settings.Newsletter, err = newsletterSubscriptionState(ctx, store, principal.IdentityID.String()); err != nil {
			return err
		}
		settings.CookieConsent, err = store.LatestCookieConsent(ctx, memberID)
		return err
	}); err != nil {
		return Settings{}, err
	}
	account, err := s.accounts.Summary(ctx, memberID)
	if err != nil {
		return Settings{}, err
	}
	settings.CanonicalEmail = account.CanonicalEmail
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

func (edit profileEdit) update() (ProfileUpdate, error) {
	update := ProfileUpdate{}
	if edit.Nickname != nil {
		value, err := normalizeMemberNickname(*edit.Nickname)
		if err != nil {
			return ProfileUpdate{}, err
		}
		update.Nickname = &value
	}
	if edit.Bio != nil {
		update.Bio = Optional{Set: true, Value: nullableTrimmed(*edit.Bio)}
	}
	if edit.Website != nil {
		update.Website = Optional{Set: true, Value: nullableTrimmed(*edit.Website)}
	}
	if edit.SocialLinks != nil {
		for key, value := range edit.SocialLinks {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
				return ProfileUpdate{}, domainerr.InvalidField("social_links", "must not contain blank keys or values")
			}
		}
		update.SocialLinks = edit.SocialLinks
	}
	update.UpdatedAt = time.Now().UTC()
	return update, nil
}

// failure classifies a profile update failure: a taken nickname and a
// missing Member are client outcomes, and the submitted values stay out of
// the failure record.
func (edit profileEdit) failure(err error, memberID string) error {
	if edit.Nickname != nil && errors.Is(err, ErrNicknameTaken) {
		nickname := strings.TrimSpace(*edit.Nickname)
		return domainerr.Duplicate("member", "nickname", nickname).WithCause(failurecause.WithPrivate(err, *edit.Nickname, nickname))
	}
	if errors.Is(err, ErrMemberNotFound) {
		return domainerr.ResourceNotFound("member", memberID)
	}
	return err
}

func nullableTrimmed(value string) *string {
	if value = strings.TrimSpace(value); value == "" {
		return nil
	}
	return &value
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

// applyProfileUpdate writes update to the Member read by current, when it
// changes it.
func applyProfileUpdate(ctx context.Context, store Store, memberID string, update ProfileUpdate, current func() (Member, error)) (profileChange, error) {
	private := update.privateValues()
	member, err := current()
	if err != nil {
		return profileChange{}, failurecause.WithPrivate(err, private...)
	}
	fields, nickname := memberProfileChangedFields(member, update)
	if len(fields) == 0 {
		return profileChange{}, nil
	}
	if err := store.UpdateProfile(ctx, memberID, update); err != nil {
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
func (s *Service) UpdateOwnProfile(ctx context.Context, command UpdateOwnProfileCommand) (memberref.Summary, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return memberref.Summary{}, err
	}
	if err := s.validateSelfMemberProfileMutation(ctx, command.Bio, command.Website, command.SocialLinks); err != nil {
		return memberref.Summary{}, err
	}
	return s.updateOwnProfile(ctx, principal, profileEdit(command))
}

// updateOwnProfile applies edit to the principal's own profile after the
// role check of the extended fields.
func (s *Service) updateOwnProfile(ctx context.Context, principal *auth.UserInfo, edit profileEdit) (memberref.Summary, error) {
	memberID := principal.MemberID.String()
	update, err := edit.update()
	if err != nil {
		return memberref.Summary{}, err
	}
	target, err := s.requireTarget(ctx, memberID)
	if err != nil {
		return memberref.Summary{}, err
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[profileChange, Store]{
		Subjects: []string{target.IdentityID},
		Access:   mutation.PrincipalFence(principal),
		Change: func(ctx context.Context, store Store) (profileChange, error) {
			if err := recheckTarget(ctx, store, memberID, target, currentTarget); err != nil {
				return profileChange{}, err
			}
			return applyProfileUpdate(ctx, store, memberID, update, func() (Member, error) { return store.OwnMember(ctx, memberID) })
		},
		Audit: func(change profileChange) mutation.Audit { return change.audit(memberID) },
	}); err != nil {
		return memberref.Summary{}, edit.failure(err, memberID)
	}
	_, profile, err := s.memberProfileByID(ctx, memberID)
	if err != nil {
		return memberref.Summary{}, err
	}
	return profile.Summary, nil
}

func (s *Service) updateMemberProfileAsAdmin(ctx context.Context, command UpdateMemberProfileCommand) error {
	edit := profileEdit{Nickname: command.Nickname, Bio: command.Bio, Website: command.Website, SocialLinks: command.SocialLinks}
	update, err := edit.update()
	if err != nil {
		return err
	}
	memberID := command.MemberID
	target, err := s.requireLinkedTarget(ctx, memberID, false)
	if err != nil {
		return err
	}
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[profileChange, Store]{
		Subjects: []string{target.IdentityID},
		Access:   freshAdmin(),
		Change: func(ctx context.Context, store Store) (profileChange, error) {
			if err := recheckTarget(ctx, store, memberID, target, linkedMember(false)); err != nil {
				return profileChange{}, err
			}
			return applyProfileUpdate(ctx, store, memberID, update, func() (Member, error) {
				return store.LinkedMember(ctx, memberID, target.IdentityID)
			})
		},
		Audit: func(change profileChange) mutation.Audit { return change.audit(memberID) },
	})
	if err != nil {
		return edit.failure(err, memberID)
	}
	return nil
}

// requireTarget resolves an active, onboarded target before a mutation.
func (s *Service) requireTarget(ctx context.Context, memberID string) (Target, error) {
	var target Target
	err := s.read(ctx, func(store Store) error {
		var err error
		target, err = store.RequireTarget(ctx, memberID)
		return err
	})
	return target, err
}

// requireLinkedTarget resolves a linked target before a mutation.
func (s *Service) requireLinkedTarget(ctx context.Context, memberID string, requireOnboarded bool) (Target, error) {
	var target Target
	err := s.read(ctx, func(store Store) error {
		var err error
		target, err = store.RequireLinkedTarget(ctx, memberID, requireOnboarded)
		return err
	})
	return target, err
}

// recheckTarget re-reads the target Member under the mutation's Identity
// lock and requires the Identity resolved before the transaction.
func recheckTarget(
	ctx context.Context,
	store Store,
	memberID string,
	target Target,
	read func(context.Context, Store, string) (Target, error),
) error {
	current, err := read(ctx, store, memberID)
	if errors.Is(err, ErrIneligible) {
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

// currentTarget reads an active, onboarded target.
func currentTarget(ctx context.Context, store Store, memberID string) (Target, error) {
	return store.CurrentTarget(ctx, memberID)
}

// linkedMember reads a Member with a live Identity link, onboarded or not.
func linkedMember(requireOnboarded bool) func(context.Context, Store, string) (Target, error) {
	return func(ctx context.Context, store Store, memberID string) (Target, error) {
		return store.CurrentLinkedTarget(ctx, memberID, requireOnboarded)
	}
}

// privateValues lists the submitted profile values, which a database error
// can echo, so the failure record redacts them.
func (update ProfileUpdate) privateValues() []string {
	var values []string
	if update.Nickname != nil {
		values = append(values, *update.Nickname)
	}
	for _, field := range []Optional{update.Bio, update.Website} {
		if field.Set && field.Value != nil {
			values = append(values, *field.Value)
		}
	}
	for _, entry := range update.SocialLinks {
		values = append(values, entry)
	}
	return values
}

func memberProfileChangedFields(current Member, update ProfileUpdate) ([]string, string) {
	changedFields := make([]string, 0, 4)
	auditNickname := ""
	if update.Nickname != nil && current.Nickname != *update.Nickname {
		changedFields = append(changedFields, "nickname")
		auditNickname = *update.Nickname
	}
	if update.Bio.Set && !sameNullableMemberProfileValue(current.Bio, update.Bio.Value) {
		changedFields = append(changedFields, "bio")
	}
	if update.Website.Set && !sameNullableMemberProfileValue(current.Website, update.Website.Value) {
		changedFields = append(changedFields, "website")
	}
	if update.SocialLinks != nil && !maps.Equal(current.SocialLinks, update.SocialLinks) {
		changedFields = append(changedFields, "social_links")
	}
	sort.Strings(changedFields)
	return changedFields, auditNickname
}

func sameNullableMemberProfileValue(current, desired *string) bool {
	if desired == nil {
		return current == nil || *current == ""
	}
	return current != nil && *current == *desired
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
	if err := s.read(ctx, func(store Store) error {
		var err error
		claimed, err = store.NicknameClaimed(ctx, nickname, principal.MemberID.String())
		return err
	}); err != nil {
		return false, failurecause.WithPrivate(err, nickname)
	}
	return !claimed, nil
}
