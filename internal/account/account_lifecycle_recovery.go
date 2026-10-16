package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	emailpkg "github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *AccountLifecycleService) RequestRecovery(ctx context.Context, email string) error {
	requestedEmail := normalizeAccountEmail(email)
	if requestedEmail == "" {
		return errs.Required("email")
	}
	token, err := generateToken()
	if err != nil {
		slog.ErrorContext(ctx, "Generate account lifecycle token failed", "operation", "RequestRecovery", failurecause.Attr(err))
		return errs.Wrap(err)
	}
	now := time.Now().UTC()
	tokenHash := crypto.HashToken(token)
	var candidate model.UserDeletionRequest
	if err := s.db.WithContext(ctx).
		Select("identity_id").
		Where("lifecycle_state IN ? AND scheduled_at > ?", []string{
			accountLifecycleStateScheduled,
			accountLifecycleStateRecoveryConfirmationPending,
		}, now).
		Where("notification_email IS NOT NULL AND LOWER(notification_email) = ?", requestedEmail).
		Take(&candidate).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		var upstreamError *auth.KratosHTTPError
		privateBody := ""
		if errors.As(err, &upstreamError) {
			privateBody = upstreamError.Body
		}
		slog.ErrorContext(ctx, "Operation failed", "operation", "RequestRecovery", failurecause.Attr(err, privateBody))
		return errs.Internal(err)
	}
	var request model.UserDeletionRequest
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.Lock(tx, candidate.IdentityID); err != nil {
			return err
		}
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("lifecycle_state IN ? AND scheduled_at > ?", []string{
				accountLifecycleStateScheduled,
				accountLifecycleStateRecoveryConfirmationPending,
			}, now).
			Where("notification_email IS NOT NULL AND LOWER(notification_email) = ?", requestedEmail).
			First(&request).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return tx.Model(&request).Updates(structured.Fields{
			"token":            tokenHash,
			"token_expires_at": now.Add(DeletionTokenExpiry),
			"lifecycle_state":  accountLifecycleStateRecoveryConfirmationPending,
			"updated_at":       now,
		}).Error
	}); err != nil {
		var upstreamError *auth.KratosHTTPError
		privateBody := ""
		if errors.As(err, &upstreamError) {
			privateBody = upstreamError.Body
		}
		slog.ErrorContext(ctx, "Operation failed", "operation", "RequestRecovery", failurecause.Attr(err, privateBody))
		return errs.Internal(err)
	}
	if request.ID == "" {
		return nil
	}

	name := strings.TrimSpace(ptrStringValue(request.NotificationName))
	job := &managev1.SendEmailEvent{
		Recipient:    requestedEmail,
		TemplateType: emailpkg.EventAccountRecoveryConfirm.String(),
		TemplateData: map[string]string{
			"name":        name,
			"confirm_url": fmt.Sprintf("%s/account/recovery/confirm?token=%s", s.baseURL, token),
			"expires_in":  "24 hours",
		},
		RecipientContext: emailpkg.SystemDirectEmailContext("account_recovery_confirm"),
		ReferenceId:      &tokenHash,
		Locale:           request.NotificationLocale,
	}
	if err := emailpkg.PublishCommand(ctx, s.publisher, job, "account-recovery-confirm:"+tokenHash); err != nil {
		var upstreamError *auth.KratosHTTPError
		privateBody := ""
		if errors.As(err, &upstreamError) {
			privateBody = upstreamError.Body
		}
		privateValues := []string{privateBody, job.Recipient, token}
		for _, value := range job.TemplateData {
			privateValues = append(privateValues, value)
		}
		slog.ErrorContext(ctx, "Operation failed", "operation", "RequestRecovery", failurecause.Attr(err, privateValues...), "template_type", job.TemplateType, "command_id", "account-recovery-confirm:"+tokenHash)
		return errs.Internal(err)
	}
	return nil
}

func (s *AccountLifecycleService) ConfirmRecovery(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errs.Required("token")
	}
	tokenHash := crypto.HashToken(token)
	now := time.Now().UTC()
	identityID, err := s.deletionRequestIdentityIDForToken(ctx, tokenHash)
	if err != nil {
		return accountLifecycleTokenError(ctx, err)
	}
	var request model.UserDeletionRequest
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.Lock(tx, identityID); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("token = ? AND token_expires_at > ? AND lifecycle_state IN ?", tokenHash, now, []string{accountLifecycleStateRecoveryConfirmationPending, accountLifecycleStateRecovered}).
			First(&request).Error; err != nil {
			return err
		}
		if request.LifecycleState != accountLifecycleStateRecovered {
			if err := tx.Model(&request).Updates(structured.Fields{
				"scheduled_at":    nil,
				"lifecycle_state": accountLifecycleStateRecovered,
				"updated_at":      gorm.Expr("GREATEST(created_at, clock_timestamp())"),
			}).Error; err != nil {
				return err
			}
			request.ScheduledAt = nil
			request.LifecycleState = accountLifecycleStateRecovered
			if s.auditWriter != nil {
				return domainaudit.AppendMember(
					ctx,
					tx,
					s.auditWriter,
					request.MemberID,
					sharedtelemetry.AuditAccountUpdated,
					func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
						return sharedtelemetry.NewAccountDeletionRecoveredAuditRecord(metadata, request.MemberID)
					},
				)
			}
		}
		return nil
	}); err != nil {
		return accountLifecycleTokenError(ctx, err)
	}
	if err := s.ensureTerminalIdentityActive(ctx, request.ID, request.IdentityID); err != nil {
		var upstreamError *auth.KratosHTTPError
		privateBody := ""
		if errors.As(err, &upstreamError) {
			privateBody = upstreamError.Body
		}
		slog.ErrorContext(ctx, "Operation failed", "operation", "ConfirmRecovery", failurecause.Attr(err, privateBody))
		return errs.Internal(fmt.Errorf("reactivate identity after recovery: %w", err))
	}

	recipient, name, err := accountLifecycleNotificationIdentity(&request)
	if err != nil {
		return err
	}
	job := &managev1.SendEmailEvent{
		Recipient:        recipient,
		TemplateType:     emailpkg.EventAccountRecoveryComplete.String(),
		TemplateData:     map[string]string{"name": name, "login_url": s.baseURL + "/login"},
		RecipientContext: emailpkg.SystemDirectEmailContext("account_recovery_complete"),
		ReferenceId:      &request.ID,
		Locale:           request.NotificationLocale,
	}
	if err := emailpkg.PublishCommand(ctx, s.publisher, job, "account-recovery-complete:"+request.ID); err != nil {
		var upstreamError *auth.KratosHTTPError
		privateBody := ""
		if errors.As(err, &upstreamError) {
			privateBody = upstreamError.Body
		}
		privateValues := []string{privateBody, job.Recipient, token}
		for _, value := range job.TemplateData {
			privateValues = append(privateValues, value)
		}
		slog.ErrorContext(ctx, "Operation failed", "operation", "ConfirmRecovery", failurecause.Attr(err, privateValues...), "template_type", job.TemplateType, "command_id", "account-recovery-complete:"+request.ID)
		return errs.Internal(err)
	}
	return nil
}
