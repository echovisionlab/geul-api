package member

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// CompleteOnboardingCommand completes the authenticated Member's onboarding
// with their chosen nickname.
type CompleteOnboardingCommand struct {
	Nickname string
}

// onboarding is the result of an onboarding mutation: the Member after it,
// and whether this call made the transition.
type onboarding struct {
	member       Member
	transitioned bool
}

// CompleteOnboarding onboards the authenticated Member, enqueues the welcome
// email in the same transaction, and returns the Member summary. Repeating a
// completed onboarding with the same nickname succeeds without a change.
func (s *Service) CompleteOnboarding(ctx context.Context, command CompleteOnboardingCommand) (Summary, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return Summary{}, err
	}
	nickname, err := normalizeMemberNickname(command.Nickname)
	if err != nil {
		return Summary{}, err
	}
	memberID, identityID := principal.MemberID.String(), principal.IdentityID.String()
	welcomeOutcome := ""
	result, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[onboarding, Store]{
		Access: mutation.PrincipalFence(principal),
		Change: func(ctx context.Context, store Store) (onboarding, error) {
			return completeMemberOnboarding(ctx, store, memberID, identityID, nickname)
		},
		Audit: func(result onboarding) mutation.Audit {
			if !result.transitioned {
				return mutation.Unchanged()
			}
			return mutation.MemberAudit(memberID, sharedtelemetry.AuditMemberUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberOnboardingCompletedAuditRecord(metadata, memberID, nickname)
			})
		},
		Outbox: func(ctx context.Context, store Store, result onboarding) error {
			if !result.transitioned {
				return nil
			}
			var err error
			welcomeOutcome, err = s.enqueueOnboardingWelcomeEmail(ctx, store, strings.TrimSpace(identityID), strings.TrimSpace(memberID), result.member)
			return err
		},
	})
	if welcomeOutcome != "" {
		s.onboardingMetrics.recordWelcomePublish(ctx, welcomeOutcome)
	}
	if err != nil {
		return Summary{}, failurecause.WithPrivate(err, nickname)
	}
	if result.transitioned && welcomeOutcome == "accepted" {
		slog.InfoContext(ctx,
			"Member onboarding welcome command confirmed",
			"domain", "member",
			"event", "member.onboarding_welcome_published",
			"outcome", "accepted",
			"identity_id", identityID,
			"member_id", memberID,
			"command_id", "welcome:"+identityID,
		)
	}
	return memberSummary(result.member, nil), nil
}

func completeMemberOnboarding(ctx context.Context, store Store, memberID, identityID, nickname string) (onboarding, error) {
	transitioned, err := store.CompleteOnboarding(ctx, memberID, identityID, nickname, time.Now().UTC())
	if err != nil {
		if errors.Is(err, ErrNicknameTaken) {
			return onboarding{}, domainerr.Duplicate("member", "nickname", nickname).WithCause(err)
		}
		return onboarding{}, fmt.Errorf("complete member onboarding: %w", err)
	}
	if !transitioned {
		current, err := resolveUnchangedMemberOnboarding(ctx, store, memberID, identityID, nickname)
		return onboarding{member: current}, err
	}
	member, err := store.MemberByID(ctx, memberID)
	if err != nil {
		return onboarding{}, fmt.Errorf("load onboarded member: %w", err)
	}
	return onboarding{member: member, transitioned: true}, nil
}

func resolveUnchangedMemberOnboarding(
	ctx context.Context,
	store Store,
	memberID string,
	identityID string,
	nickname string,
) (Member, error) {
	current, err := store.LinkedMember(ctx, memberID, identityID)
	if errors.Is(err, ErrMemberNotFound) {
		return current, domainerr.InvalidSession()
	}
	if err != nil {
		return current, fmt.Errorf("load member for unchanged onboarding: %w", err)
	}
	if !current.Onboarded {
		return current, domainerr.Precondition("member onboarding could not be completed")
	}
	if current.Nickname != nickname {
		return current, domainerr.Precondition("member onboarding is already complete")
	}
	return current, nil
}

func memberWelcomeRecipientFields(member Member) (string, *string) {
	nickname := strings.TrimSpace(member.Nickname)
	if member.PreferredLocale != nil {
		if normalized := localization.NormalizeSupportedLocale(*member.PreferredLocale); normalized != nil {
			return nickname, normalized
		}
	}
	return nickname, nil
}

// enqueueOnboardingWelcomeEmail enqueues the welcome command in the
// onboarding transaction. A failure rolls the onboarding back, so the client's
// retry sends it instead of the command being lost after the commit.
func (s *Service) enqueueOnboardingWelcomeEmail(
	ctx context.Context,
	store Store,
	identityID string,
	memberID string,
	member Member,
) (string, error) {
	delivery, err := store.WelcomeDelivery(ctx, identityID)
	if err != nil {
		return "failed", domainerr.DependencyUnavailable("account email").WithCause(
			fmt.Errorf("resolve welcome email recipient: %w", err),
		)
	}
	if delivery.Recipient == "" {
		reason := delivery.Reason
		if reason == "" {
			reason = "member_link_mismatch"
		}
		slog.InfoContext(ctx,
			"Member onboarding welcome skipped",
			"domain", "member",
			"event", "member.onboarding_welcome_skipped",
			"outcome", "skipped",
			"identity_id", identityID,
			"member_id", memberID,
			"reason", reason,
		)
		return "skipped", nil
	}
	if strings.TrimSpace(delivery.LinkedMemberID) != member.ID || member.ID != memberID {
		slog.WarnContext(ctx,
			"Member onboarding welcome skipped for changed Identity link",
			"domain", "member",
			"event", "member.onboarding_welcome_skipped",
			"outcome", "skipped",
			"identity_id", identityID,
			"member_id", memberID,
			"reason", "member_link_mismatch",
		)
		return "skipped", nil
	}
	nickname, locale := memberWelcomeRecipientFields(member)
	email := WelcomeEmail{
		CommandID: "welcome:" + identityID, IdentityID: identityID, Recipient: delivery.Recipient,
		Nickname: nickname, LoginURL: s.siteOrigin + "/login", Locale: locale,
	}
	if err := store.EnqueueWelcomeEmail(ctx, email); err != nil {
		return "failed", failurecause.WithAttrs(
			failurecause.WithPrivate(fmt.Errorf("publish welcome email command: %w", err), email.Recipient, email.Nickname, email.LoginURL),
			slog.String("command_id", email.CommandID),
		)
	}
	return "accepted", nil
}

func normalizeMemberPreferenceLocale(value string) (string, error) {
	normalized := localization.NormalizeSupportedLocale(value)
	if normalized == nil {
		return "", fmt.Errorf("must be a supported locale")
	}
	return *normalized, nil
}

// CookieConsentInput is a Member's cookie consent choice.
type CookieConsentInput struct {
	Analytics bool
	// Version defaults to 1.
	Version int32
	// Source defaults to "settings".
	Source *string
}

// UpdatePreferencesCommand changes the authenticated Member's preferences. A
// nil field stays unchanged.
type UpdatePreferencesCommand struct {
	PreferredLocale *string
	CookieConsent   *CookieConsentInput
}

// preferenceChange is what a preference update changed, for its audit record.
type preferenceChange struct {
	fields    []string
	locale    string
	consentID string
}

// UpdatePreferences applies command and returns the Member's settings.
func (s *Service) UpdatePreferences(ctx context.Context, command UpdatePreferencesCommand) (Settings, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return Settings{}, err
	}
	memberID := principal.MemberID.String()
	var preferredLocale *string
	if command.PreferredLocale != nil {
		normalized, err := normalizeMemberPreferenceLocale(*command.PreferredLocale)
		if err != nil {
			return Settings{}, domainerr.InvalidField("preferred_locale", err.Error())
		}
		preferredLocale = &normalized
	}
	var consentInput *CookieConsent
	if consent := command.CookieConsent; consent != nil {
		version := consent.Version
		if version <= 0 {
			version = 1
		}
		source := "settings"
		if consent.Source != nil && strings.TrimSpace(*consent.Source) != "" {
			source = strings.TrimSpace(*consent.Source)
		}
		consentInput = &CookieConsent{
			MemberID: memberID, Essential: true, Analytics: consent.Analytics, Version: version,
			Source: source, RecordedAt: time.Now().UTC(),
		}
	}
	if preferredLocale != nil || consentInput != nil {
		if err := s.updatePreferences(ctx, principal, preferredLocale, consentInput); err != nil {
			return Settings{}, err
		}
	}
	return s.GetSettings(ctx)
}

func (s *Service) updatePreferences(
	ctx context.Context,
	principal *auth.UserInfo,
	preferredLocale *string,
	consentInput *CookieConsent,
) error {
	memberID := principal.MemberID.String()
	target, err := s.requireTarget(ctx, memberID)
	if err != nil {
		return err
	}
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[preferenceChange, Store]{
		Subjects: []string{target.IdentityID},
		Access:   mutation.PrincipalFence(principal),
		Change: func(ctx context.Context, store Store) (preferenceChange, error) {
			if err := recheckTarget(ctx, store, memberID, target, currentTarget); err != nil {
				return preferenceChange{}, err
			}
			current, err := store.OwnMember(ctx, memberID)
			if err != nil {
				return preferenceChange{}, err
			}
			var change preferenceChange
			if preferredLocale != nil && (current.PreferredLocale == nil || *current.PreferredLocale != *preferredLocale) {
				if err := store.UpdatePreferredLocale(ctx, memberID, *preferredLocale, time.Now().UTC()); err != nil {
					return preferenceChange{}, err
				}
				change.fields = append(change.fields, "preferred_locale")
				change.locale = *preferredLocale
			}
			if consentInput != nil {
				id, err := store.InsertCookieConsent(ctx, *consentInput)
				if err != nil {
					return preferenceChange{}, err
				}
				change.consentID = id
				change.fields = append(change.fields, "cookie_consent")
			}
			sort.Strings(change.fields)
			return change, nil
		},
		Audit: func(change preferenceChange) mutation.Audit {
			if len(change.fields) == 0 {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditMemberUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberPreferencesUpdatedAuditRecord(metadata, memberID, change.fields, change.locale, change.consentID)
			})
		},
	})
	if errors.Is(err, ErrMemberNotFound) {
		return domainerr.ResourceNotFound("member", memberID)
	}
	return err
}
