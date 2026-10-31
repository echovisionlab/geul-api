package member

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func (s *MemberService) CompleteMyOnboarding(
	ctx context.Context,
	req *connect.Request[managev1.CompleteMyOnboardingRequest],
) (*connect.Response[managev1.CompleteMyOnboardingResponse], error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	nickname, err := normalizeMemberNickname(req.Msg.Nickname)
	if err != nil {
		return nil, err
	}

	var member model.Member
	transitioned := false
	welcomeOutcome := ""
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.Lock(tx, principal.IdentityID.String()); err != nil {
			return errs.Internal(fmt.Errorf("lock identity state: %w", err))
		}
		result := tx.Model(&model.Member{}).
			Where(`id = ?::uuid
				AND account_identity_id = ?::uuid
				AND deleted_at IS NULL
				AND onboarded = FALSE`, principal.MemberID.String(), principal.IdentityID.String()).
			Updates(structured.Fields{
				"nickname":   nickname,
				"onboarded":  true,
				"updated_at": time.Now().UTC(),
			})
		if result.Error != nil {
			if dberrors.IsUniqueViolation(result.Error) {
				return errs.WithCause(errs.AlreadyExists("member", "nickname", nickname), result.Error)
			}
			return errs.Internal(fmt.Errorf("complete member onboarding: %w", result.Error))
		}
		if result.RowsAffected != 1 {
			current, err := resolveUnchangedMemberOnboarding(
				tx, principal.MemberID.String(), principal.IdentityID.String(), nickname,
			)
			member = current
			return err
		}
		transitioned = true
		if err := tx.Where("id = ?::uuid", principal.MemberID.String()).Take(&member).Error; err != nil {
			return errs.Internal(fmt.Errorf("load onboarded member: %w", err))
		}
		if err := domainaudit.AppendMember(
			ctx,
			tx,
			s.auditWriter,
			principal.MemberID.String(),
			sharedtelemetry.AuditMemberUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberOnboardingCompletedAuditRecord(
					metadata,
					principal.MemberID.String(),
					nickname,
				)
			},
		); err != nil {
			return err
		}
		welcomeOutcome, err = enqueueMemberOnboardingWelcomeEmail(
			ctx,
			tx,
			s.identity,
			s.welcomeEmailPublisher,
			s.siteOrigin,
			strings.TrimSpace(principal.IdentityID.String()),
			strings.TrimSpace(principal.MemberID.String()),
			member,
			s.accountEmailProjection,
		)
		return err
	})
	if welcomeOutcome != "" {
		s.onboardingMetrics.recordWelcomePublish(ctx, welcomeOutcome)
	}
	if err != nil {
		return nil, errs.Wrap(failurecause.WithPrivate(err, nickname))
	}
	if transitioned && welcomeOutcome == "accepted" {
		slog.InfoContext(ctx,
			"Member onboarding welcome command confirmed",
			"domain", "member",
			"event", "member.onboarding_welcome_published",
			"outcome", "accepted",
			"identity_id", principal.IdentityID.String(),
			"member_id", principal.MemberID.String(),
			"command_id", "welcome:"+principal.IdentityID.String(),
		)
	}
	return connect.NewResponse(&managev1.CompleteMyOnboardingResponse{
		Member:    memberSummary(member, nil),
		Onboarded: true,
	}), nil
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
		return current, errs.InvalidSession()
	}
	if err != nil {
		return current, errs.Internal(fmt.Errorf("load member for unchanged onboarding: %w", err))
	}
	if !current.Onboarded {
		return current, errs.FailedPrecondition("member onboarding could not be completed")
	}
	if current.Nickname != nickname {
		return current, errs.FailedPrecondition("member onboarding is already complete")
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

// enqueueMemberOnboardingWelcomeEmail enqueues the welcome command in the
// onboarding transaction. A failure rolls the onboarding back, so the client's
// retry sends it instead of the command being lost after the commit.
func enqueueMemberOnboardingWelcomeEmail(
	ctx context.Context,
	tx *gorm.DB,
	identity auth.IdentityManager,
	publisher EmailCommandPublisher,
	siteOrigin string,
	identityID string,
	memberID string,
	member model.Member,
	projection AccountEmailProjection,
) (string, error) {
	if projection == nil || publisher == nil {
		return "failed", errs.Internal(errors.New("account email projection and welcome publisher are required"))
	}
	recipient, linkedMemberID, reason, err := projection.ResolveDelivery(ctx, tx, identity, identityID)
	if err != nil {
		return "failed", errs.WithCause(
			errs.DependencyUnavailable("account email"),
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
			"login_url": strings.TrimRight(strings.TrimSpace(siteOrigin), "/") + "/login",
		},
		RecipientContext: email.AccountSelectedPrimaryEmailContext(identityID),
		MessageId:        &commandID,
	}
	if locale != "" {
		job.Locale = &locale
	}
	if err := publisher.PublishSendEmailWithExecutor(ctx, executor, job); err != nil {
		privateValues := []string{job.Recipient}
		for _, value := range job.TemplateData {
			privateValues = append(privateValues, value)
		}
		return "failed", errs.Internal(failurecause.WithAttrs(
			failurecause.WithPrivate(fmt.Errorf("publish welcome email command: %w", err), privateValues...),
			slog.String("command_id", commandID),
		))
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

func (s *MemberService) UpdateMyPreferences(
	ctx context.Context,
	req *connect.Request[managev1.UpdateMyPreferencesRequest],
) (*connect.Response[managev1.UpdateMyPreferencesResponse], error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	memberID := principal.MemberID.String()
	var preferredLocale *string
	if req.Msg.PreferredLocale != nil {
		normalized, err := normalizeMemberPreferenceLocale(*req.Msg.PreferredLocale)
		if err != nil {
			return nil, errs.InvalidArgument(
				"preferred_locale",
				err.Error(),
			)
		}
		preferredLocale = &normalized
	}
	var consentInput *model.UserCookieConsent
	if consent := req.Msg.CookieConsent; consent != nil {
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
		err = authorizationtarget.WithMutation(ctx, s.db, memberID, func(mutationCtx context.Context, connection *gorm.DB) error {
			return connection.WithContext(mutationCtx).Transaction(func(tx *gorm.DB) error {
				var current model.Member
				if err := tx.Where("id = ? AND deleted_at IS NULL AND account_identity_id IS NOT NULL AND onboarded = TRUE", memberID).Take(&current).Error; err != nil {
					return err
				}
				changedFields := make([]string, 0, 2)
				if preferredLocale != nil && (current.PreferredLocale == nil || *current.PreferredLocale != *preferredLocale) {
					if err := tx.Model(&model.Member{}).Where("id = ?", memberID).Updates(structured.Fields{
						"preferred_locale": *preferredLocale,
						"updated_at":       time.Now().UTC(),
					}).Error; err != nil {
						return err
					}
					changedFields = append(changedFields, "preferred_locale")
				}
				consentID := ""
				if consentInput != nil {
					row := *consentInput
					if err := tx.Create(&row).Error; err != nil {
						return err
					}
					consentID = row.ID
					changedFields = append(changedFields, "cookie_consent")
				}
				if len(changedFields) == 0 {
					return nil
				}
				sort.Strings(changedFields)
				locale := ""
				if preferredLocale != nil && (current.PreferredLocale == nil || *current.PreferredLocale != *preferredLocale) {
					locale = *preferredLocale
				}
				return domainaudit.AppendRequest(
					mutationCtx,
					tx,
					s.auditWriter,
					sharedtelemetry.AuditMemberUpdated,
					func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
						return sharedtelemetry.NewMemberPreferencesUpdatedAuditRecord(metadata, memberID, changedFields, locale, consentID)
					},
				)
			})
		})
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.NotFound("member", memberID)
			}
			if connect.CodeOf(err) != connect.CodeUnknown {
				return nil, err
			}
			return nil, errs.Internal(err)
		}
	}
	settings, err := s.GetMySettings(ctx, connect.NewRequest(&managev1.GetMySettingsRequest{}))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.UpdateMyPreferencesResponse{Settings: settings.Msg}), nil
}
