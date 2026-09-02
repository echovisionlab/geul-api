package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	emailpkg "github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *AccountLifecycleService) RequestDeletion(
	ctx context.Context,
	memberID,
	identityID string,
) (*AccountDeletionRequestResult, error) {
	if s.memberDeletion == nil {
		return nil, errs.InternalMsg("Member deletion lifecycle is unavailable")
	}
	if err := authorizationtarget.ValidateActivePair(ctx, s.db, memberID, identityID); err != nil {
		return nil, errs.FailedPrecondition("an active member and identity link is required")
	}
	if _, err := NewAccountEmailService(s.db, s.kratosClient, s.memberEmails).
		SyncMemberEmailProjection(ctx, identityID, nil, nil); err != nil {
		return nil, errs.Internal(fmt.Errorf("sync deletion Member email projection: %w", err))
	}
	accountEmail := s.resolveVerifiedAccountEmailForDelivery(ctx, identityID, emailpkg.EventAccountDeletionConfirm.String())
	if accountEmail == nil {
		return nil, errs.FailedPrecondition("a verified Member primary email is required")
	}
	member, err := s.memberDeletion.NotificationSnapshot(ctx, s.db, memberID, identityID)
	if err != nil {
		return nil, errs.FailedPrecondition("an active member and identity link is required")
	}
	if member.PrimaryEmail == "" || !accountEmailsEqual(member.PrimaryEmail, accountEmail.Email) {
		return nil, errs.FailedPrecondition("Member primary email is not synchronized")
	}
	nickname := strings.TrimSpace(member.Nickname)
	var notificationLocale *string
	if member.Locale != nil {
		if locale := localization.NormalizeSupportedLocale(*member.Locale); locale != nil {
			notificationLocale = locale
		}
	}

	token, err := generateToken()
	if err != nil {
		return nil, errs.Wrap(err)
	}
	now := time.Now().UTC()
	tokenHash := crypto.HashToken(token)
	request := model.UserDeletionRequest{
		ID:                          uuid.NewString(),
		MemberID:                    memberID,
		IdentityID:                  identityID,
		Token:                       tokenHash,
		TokenExpiresAt:              now.Add(DeletionTokenExpiry),
		LifecycleState:              accountLifecycleStateConfirmationPending,
		NotificationEmail:           accountLifecycleStringPointer(accountEmail.Email),
		NotificationEmailVerifiedAt: &now,
		NotificationName:            accountLifecycleStringPointer(nickname),
		NotificationLocale:          notificationLocale,
		CreatedAt:                   now,
		UpdatedAt:                   now,
	}

	alreadyScheduled := false
	previousAuditState := sharedtelemetry.AuditStateNone
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.Lock(tx, identityID); err != nil {
			return err
		}
		var current model.UserDeletionRequest
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("member_id = ?::uuid AND identity_id = ?::uuid", memberID, identityID).First(&current).Error
		if err == nil && (current.LifecycleState == accountLifecycleStateScheduled || current.LifecycleState == accountLifecycleStateRecoveryConfirmationPending) {
			alreadyScheduled = true
			return nil
		}
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		if err == nil {
			previousAuditState = sharedtelemetry.AuditState(current.LifecycleState)
			if err := tx.Delete(&current).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&request).Error; err != nil {
			return err
		}
		if s.auditWriter == nil || previousAuditState == sharedtelemetry.AuditStateConfirmationPending {
			return nil
		}
		return domainaudit.AppendRequest(
			ctx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditAccountUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAccountDeletionRequestedAuditRecord(metadata, memberID, previousAuditState)
			},
		)
	}); err != nil {
		return nil, errs.Internal(err)
	}
	if alreadyScheduled {
		return &AccountDeletionRequestResult{AlreadyScheduled: true}, nil
	}

	confirmURL := fmt.Sprintf("%s/account/deletion/confirm?token=%s", s.baseURL, token)
	job := &managev1.SendEmailEvent{
		Recipient:    accountEmail.Email,
		TemplateType: emailpkg.EventAccountDeletionConfirm.String(),
		TemplateData: map[string]string{
			"name":        nickname,
			"confirm_url": confirmURL,
			"expires_in":  "24 hours",
		},
		RecipientContext: emailpkg.AccountSelectedPrimaryEmailContext(identityID),
		ReferenceId:      &tokenHash,
		Locale:           request.NotificationLocale,
	}
	if err := emailpkg.PublishCommand(ctx, s.publisher, job, "account-deletion-confirm:"+tokenHash); err != nil {
		return nil, errs.Internal(err)
	}
	return &AccountDeletionRequestResult{}, nil
}

func accountLifecycleStringPointer(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func accountLifecycleNotificationIdentity(
	request *model.UserDeletionRequest,
) (recipient string, name string, err error) {
	if request == nil {
		return "", "", errs.Internal(fmt.Errorf("account lifecycle request is required"))
	}
	recipient = normalizeAccountEmail(ptrStringValue(request.NotificationEmail))
	if recipient == "" {
		return "", "", errs.FailedPrecondition("a verified account email is required")
	}
	name = strings.TrimSpace(ptrStringValue(request.NotificationName))
	return recipient, name, nil
}

func accountLifecycleTokenError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errs.NotFoundMsg("invalid or expired token")
	}
	return errs.Internal(err)
}

func (s *AccountLifecycleService) deletionRequestIdentityIDForToken(
	ctx context.Context,
	tokenHash string,
) (string, error) {
	var request model.UserDeletionRequest
	if err := s.db.WithContext(ctx).
		Select("identity_id").
		Where("token = ?", tokenHash).
		Take(&request).Error; err != nil {
		return "", err
	}
	return request.IdentityID, nil
}

func (s *AccountLifecycleService) ConfirmDeletion(
	ctx context.Context,
	token string,
) (*AccountDeletionScheduledResult, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errs.Required("token")
	}
	tokenHash := crypto.HashToken(token)
	now := time.Now().UTC()
	identityID, err := s.deletionRequestIdentityIDForToken(ctx, tokenHash)
	if err != nil {
		return nil, accountLifecycleTokenError(err)
	}
	var request model.UserDeletionRequest

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.Lock(tx, identityID); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("token = ? AND token_expires_at > ? AND lifecycle_state IN ?", tokenHash, now, []string{accountLifecycleStateConfirmationPending, accountLifecycleStateScheduled}).
			First(&request).Error; err != nil {
			return err
		}
		if request.LifecycleState == accountLifecycleStateScheduled {
			return nil
		}
		if err := ValidateLastActiveAdminDeletionWithAuthorization(ctx, tx, request.MemberID, request.IdentityID, s.spicedb); err != nil {
			return err
		}
		scheduledAt := now.Add(DeletionGracePeriod)
		if err := tx.Model(&request).Updates(structured.Fields{
			"confirmed_at":     now,
			"scheduled_at":     scheduledAt,
			"token_expires_at": scheduledAt,
			"lifecycle_state":  accountLifecycleStateScheduled,
			"updated_at":       now,
		}).Error; err != nil {
			return err
		}
		request.ConfirmedAt = &now
		request.ScheduledAt = &scheduledAt
		request.TokenExpiresAt = scheduledAt
		request.LifecycleState = accountLifecycleStateScheduled
		if s.auditWriter != nil {
			return domainaudit.AppendMember(
				ctx,
				tx,
				s.auditWriter,
				request.MemberID,
				sharedtelemetry.AuditAccountUpdated,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewAccountDeletionScheduledAuditRecord(
						metadata, request.MemberID, sharedtelemetry.AuditStateConfirmationPending,
					)
				},
			)
		}
		return nil
	}); err != nil {
		if errors.Is(err, ErrLastActiveAdminDeletion) {
			return nil, errs.FailedPrecondition("the last active admin cannot be deleted")
		}
		return nil, accountLifecycleTokenError(err)
	}
	if err := s.ensureScheduledIdentityInactive(ctx, request.ID, request.IdentityID); err != nil {
		return nil, errs.Internal(fmt.Errorf("converge scheduled deletion identity: %w", err))
	}

	if request.ScheduledAt == nil {
		return nil, errs.Internal(fmt.Errorf("scheduled deletion timestamp is missing"))
	}
	recipient, name, err := accountLifecycleNotificationIdentity(&request)
	if err != nil {
		return nil, err
	}
	job := &managev1.SendEmailEvent{
		Recipient:    recipient,
		TemplateType: emailpkg.EventAccountDeletionScheduled.String(),
		TemplateData: map[string]string{
			"name":           name,
			"scheduled_date": request.ScheduledAt.Format("2006-01-02"),
			"grace_period":   "30 days",
			"cancel_url":     fmt.Sprintf("%s/account/deletion/cancel?token=%s", s.baseURL, token),
			"recover_url":    s.baseURL + "/account/recover",
		},
		RecipientContext: emailpkg.SystemDirectEmailContext("account_deletion_scheduled"),
		ReferenceId:      &request.ID,
		Locale:           request.NotificationLocale,
	}
	if err := emailpkg.PublishCommand(ctx, s.publisher, job, "account-deletion-scheduled:"+request.ID); err != nil {
		return nil, errs.Internal(err)
	}
	return &AccountDeletionScheduledResult{ScheduledAt: *request.ScheduledAt}, nil
}

func (s *AccountLifecycleService) CancelDeletion(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errs.Required("token")
	}
	tokenHash := crypto.HashToken(token)
	now := time.Now().UTC()
	identityID, err := s.deletionRequestIdentityIDForToken(ctx, tokenHash)
	if err != nil {
		return accountLifecycleTokenError(err)
	}
	var request model.UserDeletionRequest
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.Lock(tx, identityID); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("token = ? AND token_expires_at > ? AND lifecycle_state IN ?", tokenHash, now, []string{accountLifecycleStateScheduled, accountLifecycleStateCancelled}).
			First(&request).Error; err != nil {
			return err
		}
		if request.LifecycleState != accountLifecycleStateCancelled {
			if err := tx.Model(&request).Updates(structured.Fields{
				"scheduled_at":    nil,
				"lifecycle_state": accountLifecycleStateCancelled,
				"updated_at":      gorm.Expr("GREATEST(created_at, clock_timestamp())"),
			}).Error; err != nil {
				return err
			}
			request.ScheduledAt = nil
			request.LifecycleState = accountLifecycleStateCancelled
			if s.auditWriter != nil {
				return domainaudit.AppendMember(
					ctx,
					tx,
					s.auditWriter,
					request.MemberID,
					sharedtelemetry.AuditAccountUpdated,
					func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
						return sharedtelemetry.NewAccountDeletionCancelledAuditRecord(metadata, request.MemberID)
					},
				)
			}
		}
		return nil
	}); err != nil {
		return accountLifecycleTokenError(err)
	}
	// The terminal domain transition commits first. Explicit retry and the
	// leader-elected reconciler share the same guarded convergence path.
	if err := s.ensureTerminalIdentityActive(ctx, request.ID, request.IdentityID); err != nil {
		return errs.Internal(fmt.Errorf("reactivate identity after deletion cancellation: %w", err))
	}

	recipient, name, err := accountLifecycleNotificationIdentity(&request)
	if err != nil {
		return err
	}
	job := &managev1.SendEmailEvent{
		Recipient:        recipient,
		TemplateType:     emailpkg.EventAccountDeletionCancelled.String(),
		TemplateData:     map[string]string{"name": name, "login_url": s.baseURL + "/login"},
		RecipientContext: emailpkg.AccountSelectedPrimaryEmailContext(request.IdentityID),
		ReferenceId:      &request.ID,
		Locale:           request.NotificationLocale,
	}
	if err := emailpkg.PublishCommand(ctx, s.publisher, job, "account-deletion-cancelled:"+request.ID); err != nil {
		return errs.Internal(err)
	}
	return nil
}
