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
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

// CompleteOnboardingCommand completes the authenticated Member's onboarding
// with their chosen nickname.
type CompleteOnboardingCommand struct {
	Nickname string
}

// onboarding is the result of an onboarding mutation: the Member after it,
// and whether this call made the transition.
type onboarding struct {
	member       model.Member
	transitioned bool
}

// CompleteOnboarding onboards the authenticated Member, enqueues the welcome
// email in the same transaction, and returns the Member summary. Repeating a
// completed onboarding with the same nickname succeeds without a change.
func (s *Service) CompleteOnboarding(ctx context.Context, command CompleteOnboardingCommand) (*commonv1.MemberSummary, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	nickname, err := normalizeMemberNickname(command.Nickname)
	if err != nil {
		return nil, err
	}
	memberID, identityID := principal.MemberID.String(), principal.IdentityID.String()
	welcomeOutcome := ""
	result, err := mutation.Run(ctx, s.mutations, mutation.Spec[onboarding]{
		Access: mutation.PrincipalFence(principal),
		Change: func(_ context.Context, tx *gorm.DB) (onboarding, error) {
			return completeMemberOnboarding(tx, memberID, identityID, nickname)
		},
		Audit: func(result onboarding) mutation.Audit {
			if !result.transitioned {
				return mutation.Unchanged()
			}
			return mutation.MemberAudit(memberID, sharedtelemetry.AuditMemberUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberOnboardingCompletedAuditRecord(metadata, memberID, nickname)
			})
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, result onboarding) error {
			if !result.transitioned {
				return nil
			}
			var err error
			welcomeOutcome, err = s.enqueueOnboardingWelcomeEmail(ctx, tx, strings.TrimSpace(identityID), strings.TrimSpace(memberID), result.member)
			return err
		},
	})
	if welcomeOutcome != "" {
		s.onboardingMetrics.recordWelcomePublish(ctx, welcomeOutcome)
	}
	if err != nil {
		return nil, failurecause.WithPrivate(err, nickname)
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

func completeMemberOnboarding(tx *gorm.DB, memberID, identityID, nickname string) (onboarding, error) {
	result := tx.Model(&model.Member{}).
		Where(`id = ?::uuid
			AND account_identity_id = ?::uuid
			AND deleted_at IS NULL
			AND onboarded = FALSE`, memberID, identityID).
		Updates(structured.Fields{
			"nickname":   nickname,
			"onboarded":  true,
			"updated_at": time.Now().UTC(),
		})
	if result.Error != nil {
		if dberrors.IsUniqueViolation(result.Error) {
			return onboarding{}, domainerr.Duplicate("member", "nickname", nickname).WithCause(result.Error)
		}
		return onboarding{}, fmt.Errorf("complete member onboarding: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		current, err := resolveUnchangedMemberOnboarding(tx, memberID, identityID, nickname)
		return onboarding{member: current}, err
	}
	var member model.Member
	if err := tx.Where("id = ?::uuid", memberID).Take(&member).Error; err != nil {
		return onboarding{}, fmt.Errorf("load onboarded member: %w", err)
	}
	return onboarding{member: member, transitioned: true}, nil
}

func resolveUnchangedMemberOnboarding(
	tx *gorm.DB,
	memberID string,
	identityID string,
	nickname string,
) (model.Member, error) {
	var current model.Member
	err := tx.Where(
		"id = ?::uuid AND account_identity_id = ?::uuid AND deleted_at IS NULL",
		memberID,
		identityID,
	).Take(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
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

func memberWelcomeRecipientFields(member model.Member) (string, string) {
	nickname := strings.TrimSpace(member.Nickname)
	locale := ""
	if member.PreferredLocale != nil {
		if normalized := localization.NormalizeSupportedLocale(*member.PreferredLocale); normalized != nil {
			locale = *normalized
		}
	}
	return nickname, locale
}

// enqueueOnboardingWelcomeEmail enqueues the welcome command in the
// onboarding transaction. A failure rolls the onboarding back, so the client's
// retry sends it instead of the command being lost after the commit.
func (s *Service) enqueueOnboardingWelcomeEmail(
	ctx context.Context,
	tx *gorm.DB,
	identityID string,
	memberID string,
	member model.Member,
) (string, error) {
	recipient, linkedMemberID, reason, err := s.accountEmails.ResolveDelivery(ctx, tx, s.identity, identityID)
	if err != nil {
		return "failed", domainerr.DependencyUnavailable("account email").WithCause(
			fmt.Errorf("resolve welcome email recipient: %w", err),
		)
	}
	if recipient == "" {
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
	if strings.TrimSpace(linkedMemberID) != member.ID || member.ID != memberID {
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
	executor, err := contentsignal.TransactionExecutor(tx)
	if err != nil {
		return "failed", err
	}
	nickname, locale := memberWelcomeRecipientFields(member)
	commandID := "welcome:" + identityID
	job := &managev1.SendEmailEvent{
		Recipient:    recipient,
		TemplateType: email.EventWelcome.String(),
		TemplateData: map[string]string{
			"name":      nickname,
			"login_url": s.siteOrigin + "/login",
		},
		RecipientContext: email.AccountSelectedPrimaryEmailContext(identityID),
		MessageId:        &commandID,
	}
	if locale != "" {
		job.Locale = &locale
	}
	if err := s.welcomeEmails.PublishSendEmailWithExecutor(ctx, executor, job); err != nil {
		privateValues := []string{job.Recipient}
		for _, value := range job.TemplateData {
			privateValues = append(privateValues, value)
		}
		return "failed", failurecause.WithAttrs(
			failurecause.WithPrivate(fmt.Errorf("publish welcome email command: %w", err), privateValues...),
			slog.String("command_id", commandID),
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
func (s *Service) UpdatePreferences(ctx context.Context, command UpdatePreferencesCommand) (*managev1.GetMySettingsResponse, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	memberID := principal.MemberID.String()
	var preferredLocale *string
	if command.PreferredLocale != nil {
		normalized, err := normalizeMemberPreferenceLocale(*command.PreferredLocale)
		if err != nil {
			return nil, domainerr.InvalidField("preferred_locale", err.Error())
		}
		preferredLocale = &normalized
	}
	var consentInput *model.UserCookieConsent
	if consent := command.CookieConsent; consent != nil {
		version := consent.Version
		if version <= 0 {
			version = 1
		}
		source := "settings"
		if consent.Source != nil && strings.TrimSpace(*consent.Source) != "" {
			source = strings.TrimSpace(*consent.Source)
		}
		consentInput = &model.UserCookieConsent{
			MemberID: memberID, Essential: true, Analytics: consent.Analytics, ConsentVersion: version,
			Source: source, RecordedAt: time.Now().UTC(),
		}
	}
	if preferredLocale != nil || consentInput != nil {
		if err := s.updatePreferences(ctx, principal, preferredLocale, consentInput); err != nil {
			return nil, err
		}
	}
	return s.GetSettings(ctx)
}

func (s *Service) updatePreferences(
	ctx context.Context,
	principal *auth.UserInfo,
	preferredLocale *string,
	consentInput *model.UserCookieConsent,
) error {
	memberID := principal.MemberID.String()
	target, err := authorizationtarget.Require(ctx, s.db, memberID)
	if err != nil {
		return err
	}
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[preferenceChange]{
		Subjects: []string{target.IdentityID},
		Access:   mutation.PrincipalFence(principal),
		Change: func(_ context.Context, tx *gorm.DB) (preferenceChange, error) {
			if err := recheckTarget(tx, memberID, target, authorizationtarget.ForMember); err != nil {
				return preferenceChange{}, err
			}
			var current model.Member
			if err := tx.Where("id = ? AND deleted_at IS NULL AND account_identity_id IS NOT NULL AND onboarded = TRUE", memberID).Take(&current).Error; err != nil {
				return preferenceChange{}, err
			}
			var change preferenceChange
			if preferredLocale != nil && (current.PreferredLocale == nil || *current.PreferredLocale != *preferredLocale) {
				if err := tx.Model(&model.Member{}).Where("id = ?", memberID).Updates(structured.Fields{
					"preferred_locale": *preferredLocale,
					"updated_at":       time.Now().UTC(),
				}).Error; err != nil {
					return preferenceChange{}, err
				}
				change.fields = append(change.fields, "preferred_locale")
				change.locale = *preferredLocale
			}
			if consentInput != nil {
				row := *consentInput
				if err := tx.Create(&row).Error; err != nil {
					return preferenceChange{}, err
				}
				change.consentID = row.ID
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
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainerr.ResourceNotFound("member", memberID)
	}
	return err
}
