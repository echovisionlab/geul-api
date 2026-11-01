package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	emailpkg "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ConfirmAccountDeletionCommand confirms a deletion with its emailed token.
type ConfirmAccountDeletionCommand struct{ Token string }

// CancelAccountDeletionCommand cancels a scheduled deletion with its emailed
// token.
type CancelAccountDeletionCommand struct{ Token string }

// deletionRequestStored is the outcome of storing a deletion request: the
// stored row, or that a deletion was already scheduled.
type deletionRequestStored struct {
	alreadyScheduled bool
	previous         sharedtelemetry.AuditState
}

// requestDeletion stores a confirmation-pending deletion request for the
// principal and emails its confirmation link.
func (s *Service) requestDeletion(ctx context.Context, principal *auth.UserInfo) (*AccountDeletionRequestResult, error) {
	memberID, identityID := principal.MemberID.String(), principal.IdentityID.String()
	if err := authorizationtarget.ValidateActivePair(ctx, s.db, memberID, identityID); err != nil {
		return nil, accountDeletionLinkError(err)
	}
	notification, err := s.deletionNotification(ctx, memberID, identityID)
	if err != nil {
		return nil, err
	}
	token, err := generateToken()
	if err != nil {
		return nil, err
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
		NotificationEmail:           accountLifecycleStringPointer(notification.email),
		NotificationEmailVerifiedAt: &now,
		NotificationName:            accountLifecycleStringPointer(notification.name),
		NotificationLocale:          notification.locale,
		CreatedAt:                   now,
		UpdatedAt:                   now,
	}
	stored, err := mutation.Run(ctx, s.mutations, mutation.Spec[deletionRequestStored]{
		Access: mutation.PrincipalFence(principal),
		Change: func(_ context.Context, tx *gorm.DB) (deletionRequestStored, error) {
			return storeDeletionRequest(tx, &request)
		},
		Audit: func(stored deletionRequestStored) mutation.Audit {
			// A repeated request replaces the pending confirmation without a
			// new transition.
			if stored.alreadyScheduled || stored.previous == sharedtelemetry.AuditStateConfirmationPending {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditAccountUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAccountDeletionRequestedAuditRecord(metadata, memberID, stored.previous)
			})
		},
	})
	if err != nil {
		return nil, err
	}
	if stored.alreadyScheduled {
		return &AccountDeletionRequestResult{AlreadyScheduled: true}, nil
	}

	confirmURL := fmt.Sprintf("%s/account/deletion/confirm?token=%s", s.baseURL, token)
	job := &managev1.SendEmailEvent{
		Recipient:    notification.email,
		TemplateType: emailpkg.EventAccountDeletionConfirm.String(),
		TemplateData: map[string]string{
			"name":        notification.name,
			"confirm_url": confirmURL,
			"expires_in":  "24 hours",
		},
		RecipientContext: emailpkg.AccountSelectedPrimaryEmailContext(identityID),
		ReferenceId:      &tokenHash,
		Locale:           request.NotificationLocale,
	}
	if err := emailpkg.PublishCommand(ctx, s.publisher, job, "account-deletion-confirm:"+tokenHash); err != nil {
		return nil, emailPublishFailure(err, job, "account-deletion-confirm:"+tokenHash, token)
	}
	return &AccountDeletionRequestResult{}, nil
}

// deletionNotificationSnapshot is the recipient a deletion request records
// before the Identity is disabled.
type deletionNotificationSnapshot struct {
	email  string
	name   string
	locale *string
}

// deletionNotification synchronizes the Member email projection and reads
// the recipient, name and locale of the deletion emails.
func (s *Service) deletionNotification(ctx context.Context, memberID, identityID string) (deletionNotificationSnapshot, error) {
	if _, err := NewAccountEmailService(s.db, s.identity, s.memberEmails).
		SyncMemberEmailProjection(ctx, identityID, nil, nil); err != nil {
		return deletionNotificationSnapshot{}, fmt.Errorf("sync deletion Member email projection: %w", err)
	}
	accountEmail, _, err := ResolveMemberPrimaryEmailForIdentity(ctx, s.db, s.memberEmails, s.identity, identityID)
	if err != nil {
		return deletionNotificationSnapshot{}, accountIdentityLookupError{cause: err}
	}
	if accountEmail == nil {
		return deletionNotificationSnapshot{}, domainerr.Precondition("a verified Member primary email is required")
	}
	member, err := s.memberDeletion.NotificationSnapshot(ctx, s.db, memberID, identityID)
	if err != nil {
		return deletionNotificationSnapshot{}, accountDeletionLinkError(err)
	}
	if member.PrimaryEmail == "" || !accountEmailsEqual(member.PrimaryEmail, accountEmail.Email) {
		return deletionNotificationSnapshot{}, domainerr.Precondition("Member primary email is not synchronized")
	}
	snapshot := deletionNotificationSnapshot{email: accountEmail.Email, name: strings.TrimSpace(member.Nickname)}
	if member.Locale != nil {
		snapshot.locale = localization.NormalizeSupportedLocale(*member.Locale)
	}
	return snapshot, nil
}

// storeDeletionRequest replaces the pair's request with request unless a
// deletion is already scheduled.
func storeDeletionRequest(tx *gorm.DB, request *model.UserDeletionRequest) (deletionRequestStored, error) {
	stored := deletionRequestStored{previous: sharedtelemetry.AuditStateNone}
	var current model.UserDeletionRequest
	err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("member_id = ?::uuid AND identity_id = ?::uuid", request.MemberID, request.IdentityID).
		First(&current).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
	case err != nil:
		return stored, err
	case current.LifecycleState == accountLifecycleStateScheduled || current.LifecycleState == accountLifecycleStateRecoveryConfirmationPending:
		return deletionRequestStored{alreadyScheduled: true}, nil
	default:
		stored.previous = sharedtelemetry.AuditState(current.LifecycleState)
		if err := tx.Delete(&current).Error; err != nil {
			return stored, err
		}
	}
	return stored, tx.Create(request).Error
}

// accountDeletionLinkError answers a deletion request whose Member and
// Identity are no longer an active pair, keeping the reason for the record.
func accountDeletionLinkError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, authorizationtarget.ErrIneligible) {
		return domainerr.Precondition("an active member and identity link is required").WithCause(err)
	}
	return fmt.Errorf("resolve account deletion link: %w", err)
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
		return "", "", errors.New("account lifecycle request is required")
	}
	recipient = normalizeAccountEmail(ptrStringValue(request.NotificationEmail))
	if recipient == "" {
		return "", "", domainerr.Precondition("a verified account email is required")
	}
	name = strings.TrimSpace(ptrStringValue(request.NotificationName))
	return recipient, name, nil
}

func accountLifecycleTokenError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainerr.New(domainerr.NotFound, "", "invalid or expired token").WithCause(err)
	}
	return err
}

func (s *Service) deletionRequestIdentityIDForToken(
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

// tokenTransition moves the deletion request an emailed token names from
// one of from to its next state. The token is the caller's authority: the
// request is read under its Identity fence, and a missing or expired token
// answers before anything changes.
type tokenTransition struct {
	token string
	from  []string
	// apply changes the locked request and reports whether it changed.
	apply func(ctx context.Context, tx *gorm.DB, request *model.UserDeletionRequest) (bool, error)
	audit func(memberID string) mutation.Audit
}

type tokenTransitioned struct {
	request model.UserDeletionRequest
	changed bool
}

func (s *Service) transitionByToken(ctx context.Context, transition tokenTransition, now time.Time) (model.UserDeletionRequest, error) {
	token := strings.TrimSpace(transition.token)
	if token == "" {
		return model.UserDeletionRequest{}, domainerr.Required("token")
	}
	tokenHash := crypto.HashToken(token)
	identityID, err := s.deletionRequestIdentityIDForToken(ctx, tokenHash)
	if err != nil {
		return model.UserDeletionRequest{}, accountLifecycleTokenError(err)
	}
	var request model.UserDeletionRequest
	result, err := mutation.Run(ctx, s.mutations, mutation.Spec[tokenTransitioned]{
		Lock: func(_ context.Context, tx *gorm.DB) error { return identitystate.Lock(tx, identityID) },
		Access: mutation.Checked(func(_ context.Context, tx *gorm.DB) error {
			return tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
				Where("token = ? AND token_expires_at > ? AND lifecycle_state IN ?", tokenHash, now, transition.from).
				First(&request).Error
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (tokenTransitioned, error) {
			changed, err := transition.apply(ctx, tx, &request)
			return tokenTransitioned{request: request, changed: changed}, err
		},
		Audit: func(result tokenTransitioned) mutation.Audit {
			if !result.changed {
				return mutation.Unchanged()
			}
			return transition.audit(result.request.MemberID)
		},
	})
	if err != nil {
		if errors.Is(err, ErrLastActiveAdminDeletion) {
			return model.UserDeletionRequest{}, domainerr.Precondition("the last active admin cannot be deleted")
		}
		return model.UserDeletionRequest{}, accountLifecycleTokenError(err)
	}
	return result.request, nil
}

// accountDeletionMemberAudit attributes a token-authenticated transition to
// the Member the token belongs to.
func accountDeletionMemberAudit(memberID string, build func(sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error)) mutation.Audit {
	return mutation.MemberAudit(memberID, sharedtelemetry.AuditAccountUpdated, build)
}

// ConfirmAccountDeletion schedules a confirmed deletion after the grace
// period and deactivates the Identity until then.
func (s *Service) ConfirmAccountDeletion(ctx context.Context, command ConfirmAccountDeletionCommand) (AccountDeletionScheduledResult, error) {
	now := dbtime.Now()
	request, err := s.transitionByToken(ctx, tokenTransition{
		token: command.Token,
		from:  []string{accountLifecycleStateConfirmationPending, accountLifecycleStateScheduled},
		apply: s.scheduleConfirmedDeletion(now),
		audit: func(memberID string) mutation.Audit {
			return accountDeletionMemberAudit(memberID, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAccountDeletionScheduledAuditRecord(metadata, memberID, sharedtelemetry.AuditStateConfirmationPending)
			})
		},
	}, now)
	if err != nil {
		return AccountDeletionScheduledResult{}, err
	}
	if err := s.ensureScheduledIdentityInactive(ctx, request.ID, request.IdentityID); err != nil {
		return AccountDeletionScheduledResult{}, fmt.Errorf("converge scheduled deletion identity: %w", err)
	}
	if request.ScheduledAt == nil {
		return AccountDeletionScheduledResult{}, errors.New("scheduled deletion timestamp is missing")
	}
	recipient, name, err := accountLifecycleNotificationIdentity(&request)
	if err != nil {
		return AccountDeletionScheduledResult{}, err
	}
	job := &managev1.SendEmailEvent{
		Recipient:    recipient,
		TemplateType: emailpkg.EventAccountDeletionScheduled.String(),
		TemplateData: map[string]string{
			"name":           name,
			"scheduled_date": request.ScheduledAt.Format("2006-01-02"),
			"grace_period":   "30 days",
			"cancel_url":     fmt.Sprintf("%s/account/deletion/cancel?token=%s", s.baseURL, strings.TrimSpace(command.Token)),
			"recover_url":    s.baseURL + "/account/recover",
		},
		RecipientContext: emailpkg.SystemDirectEmailContext("account_deletion_scheduled"),
		ReferenceId:      &request.ID,
		Locale:           request.NotificationLocale,
	}
	if err := emailpkg.PublishCommand(ctx, s.publisher, job, "account-deletion-scheduled:"+request.ID); err != nil {
		return AccountDeletionScheduledResult{}, emailPublishFailure(err, job, "account-deletion-scheduled:"+request.ID, strings.TrimSpace(command.Token))
	}
	return AccountDeletionScheduledResult{ScheduledAt: *request.ScheduledAt}, nil
}

// scheduleConfirmedDeletion schedules a confirmation-pending request for the
// end of the grace period; a request already scheduled is unchanged. The
// last-admin fence keeps the last active admin from confirming.
func (s *Service) scheduleConfirmedDeletion(now time.Time) func(context.Context, *gorm.DB, *model.UserDeletionRequest) (bool, error) {
	return func(ctx context.Context, tx *gorm.DB, request *model.UserDeletionRequest) (bool, error) {
		if request.LifecycleState == accountLifecycleStateScheduled {
			return false, nil
		}
		if err := ValidateLastActiveAdminDeletionWithAuthorization(ctx, tx, request.MemberID, request.IdentityID, s.spicedb); err != nil {
			return false, err
		}
		scheduledAt := now.Add(DeletionGracePeriod)
		if err := tx.Model(request).Updates(structured.Fields{
			"confirmed_at":     now,
			"scheduled_at":     scheduledAt,
			"token_expires_at": scheduledAt,
			"lifecycle_state":  accountLifecycleStateScheduled,
			"updated_at":       now,
		}).Error; err != nil {
			return false, err
		}
		request.ConfirmedAt = &now
		request.ScheduledAt = &scheduledAt
		request.TokenExpiresAt = scheduledAt
		request.LifecycleState = accountLifecycleStateScheduled
		return true, nil
	}
}

// CancelAccountDeletion cancels a scheduled deletion and reactivates the
// Identity.
func (s *Service) CancelAccountDeletion(ctx context.Context, command CancelAccountDeletionCommand) error {
	request, err := s.transitionByToken(ctx, tokenTransition{
		token: command.Token,
		from:  []string{accountLifecycleStateScheduled, accountLifecycleStateCancelled},
		apply: terminalTransition(accountLifecycleStateCancelled),
		audit: func(memberID string) mutation.Audit {
			return accountDeletionMemberAudit(memberID, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAccountDeletionCancelledAuditRecord(metadata, memberID)
			})
		},
	}, time.Now().UTC())
	if err != nil {
		return err
	}
	// The terminal domain transition commits first. Explicit retry and the
	// leader-elected reconciler share the same guarded convergence path.
	if err := s.ensureTerminalIdentityActive(ctx, request.ID, request.IdentityID); err != nil {
		return fmt.Errorf("reactivate identity after deletion cancellation: %w", err)
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
		return emailPublishFailure(err, job, "account-deletion-cancelled:"+request.ID, strings.TrimSpace(command.Token))
	}
	return nil
}

// terminalTransition ends a deletion request in state, clearing its
// deadline; a request already there is unchanged.
func terminalTransition(state string) func(context.Context, *gorm.DB, *model.UserDeletionRequest) (bool, error) {
	return func(_ context.Context, tx *gorm.DB, request *model.UserDeletionRequest) (bool, error) {
		if request.LifecycleState == state {
			return false, nil
		}
		if err := tx.Model(request).Updates(structured.Fields{
			"scheduled_at":    nil,
			"lifecycle_state": state,
			"updated_at":      gorm.Expr("GREATEST(created_at, clock_timestamp())"),
		}).Error; err != nil {
			return false, err
		}
		request.ScheduledAt = nil
		request.LifecycleState = state
		return true, nil
	}
}
