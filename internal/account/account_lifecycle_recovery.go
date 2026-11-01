package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	emailpkg "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RequestAccountRecoveryCommand asks for a recovery link for the account
// pending deletion under Email.
type RequestAccountRecoveryCommand struct{ Email string }

// ConfirmAccountRecoveryCommand recovers an account with its emailed token.
type ConfirmAccountRecoveryCommand struct{ Token string }

// RequestAccountRecovery emails a recovery link to the address a pending
// deletion recorded. An address without a pending deletion answers the same,
// so the request reveals nothing about which accounts exist.
func (s *Service) RequestAccountRecovery(ctx context.Context, command RequestAccountRecoveryCommand) error {
	requestedEmail := normalizeAccountEmail(command.Email)
	if requestedEmail == "" {
		return domainerr.Required("email")
	}
	token, err := generateToken()
	if err != nil {
		return err
	}
	tokenHash := crypto.HashToken(token)
	request, err := s.startRecovery(ctx, requestedEmail, tokenHash)
	if err != nil {
		return err
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
		return emailPublishFailure(err, job, "account-recovery-confirm:"+tokenHash, token)
	}
	return nil
}

// startRecovery moves the pending deletion recorded for requestedEmail to
// recovery confirmation under tokenHash. It returns the zero request when no
// deletion of that address is pending.
func (s *Service) startRecovery(ctx context.Context, requestedEmail, tokenHash string) (model.UserDeletionRequest, error) {
	now := time.Now().UTC()
	pending := func(db *gorm.DB) *gorm.DB {
		return db.Where("lifecycle_state IN ? AND scheduled_at > ?", []string{
			accountLifecycleStateScheduled,
			accountLifecycleStateRecoveryConfirmationPending,
		}, now).Where("notification_email IS NOT NULL AND LOWER(notification_email) = ?", requestedEmail)
	}
	var candidate model.UserDeletionRequest
	if err := pending(s.db.WithContext(ctx)).Select("identity_id").Take(&candidate).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.UserDeletionRequest{}, nil
		}
		return model.UserDeletionRequest{}, err
	}
	var request model.UserDeletionRequest
	if _, err := mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Lock: func(_ context.Context, tx *gorm.DB) error { return identitystate.Lock(tx, candidate.IdentityID) },
		// The recovery link goes only to the address the deletion recorded,
		// so the address itself is the request's authority.
		Access: mutation.Checked(func(_ context.Context, tx *gorm.DB) error {
			err := pending(tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})).First(&request).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}),
		Change: func(_ context.Context, tx *gorm.DB) (struct{}, error) {
			if request.ID == "" {
				return struct{}{}, nil
			}
			return struct{}{}, tx.Model(&request).Updates(structured.Fields{
				"token":            tokenHash,
				"token_expires_at": now.Add(DeletionTokenExpiry),
				"lifecycle_state":  accountLifecycleStateRecoveryConfirmationPending,
				"updated_at":       now,
			}).Error
		},
		// Only a confirmed recovery is a lifecycle transition worth a record.
		Audit: func(struct{}) mutation.Audit { return mutation.Unchanged() },
	}); err != nil {
		return model.UserDeletionRequest{}, err
	}
	return request, nil
}

// ConfirmAccountRecovery cancels the pending deletion the token names and
// reactivates the Identity.
func (s *Service) ConfirmAccountRecovery(ctx context.Context, command ConfirmAccountRecoveryCommand) error {
	request, err := s.transitionByToken(ctx, tokenTransition{
		token: command.Token,
		from:  []string{accountLifecycleStateRecoveryConfirmationPending, accountLifecycleStateRecovered},
		apply: terminalTransition(accountLifecycleStateRecovered),
		audit: func(memberID string) mutation.Audit {
			return accountDeletionMemberAudit(memberID, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAccountDeletionRecoveredAuditRecord(metadata, memberID)
			})
		},
	}, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := s.ensureTerminalIdentityActive(ctx, request.ID, request.IdentityID); err != nil {
		return fmt.Errorf("reactivate identity after recovery: %w", err)
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
		return emailPublishFailure(err, job, "account-recovery-complete:"+request.ID, strings.TrimSpace(command.Token))
	}
	return nil
}
