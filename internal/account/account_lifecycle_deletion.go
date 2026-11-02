package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	emailpkg "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
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
	if err := s.read(ctx, func(store Store) error { return store.ValidateActivePair(ctx, memberID, identityID) }); err != nil {
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
	request := DeletionRequest{
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
	stored, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[deletionRequestStored, Store]{
		Access: mutation.PrincipalFence(principal),
		Change: func(ctx context.Context, store Store) (deletionRequestStored, error) {
			return storeDeletionRequest(ctx, store, request)
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
	job := Email{
		Recipient:    notification.email,
		TemplateType: emailpkg.EventAccountDeletionConfirm.String(),
		TemplateData: map[string]string{
			"name":        notification.name,
			"confirm_url": confirmURL,
			"expires_in":  "24 hours",
		},
		AccountIdentityID: identityID,
		ReferenceID:       &tokenHash,
		Locale:            request.NotificationLocale,
		MessageID:         "account-deletion-confirm:" + tokenHash,
	}
	if err := s.emails.PublishEmail(ctx, job); err != nil {
		return nil, emailPublishFailure(err, job, token)
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
	if _, err := newAccountEmailService(s.identity, s.transact).
		SyncMemberEmailProjection(ctx, identityID, nil, nil); err != nil {
		return deletionNotificationSnapshot{}, fmt.Errorf("sync deletion Member email projection: %w", err)
	}
	accountEmail, _, err := ResolveMemberPrimaryEmailForIdentity(ctx, s.read, s.identity, identityID)
	if err != nil {
		return deletionNotificationSnapshot{}, accountIdentityLookupError{cause: err}
	}
	if accountEmail == nil {
		return deletionNotificationSnapshot{}, domainerr.Precondition("a verified Member primary email is required")
	}
	var member MemberDeletionNotification
	if err := s.read(ctx, func(store Store) error {
		var err error
		member, err = store.MemberDeletionNotification(ctx, memberID, identityID)
		return err
	}); err != nil {
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
func storeDeletionRequest(ctx context.Context, store Store, request DeletionRequest) (deletionRequestStored, error) {
	stored := deletionRequestStored{previous: sharedtelemetry.AuditStateNone}
	current, found, err := store.LockPairDeletionRequest(ctx, request.MemberID, request.IdentityID)
	switch {
	case err != nil:
		return stored, err
	case !found:
	case current.LifecycleState == accountLifecycleStateScheduled || current.LifecycleState == accountLifecycleStateRecoveryConfirmationPending:
		return deletionRequestStored{alreadyScheduled: true}, nil
	default:
		stored.previous = sharedtelemetry.AuditState(current.LifecycleState)
		if err := store.DeleteDeletionRequest(ctx, current.ID); err != nil {
			return stored, err
		}
	}
	return stored, store.InsertDeletionRequest(ctx, request)
}

// accountDeletionLinkError answers a deletion request whose Member and
// Identity are no longer an active pair, keeping the reason for the record.
func accountDeletionLinkError(err error) error {
	if errors.Is(err, ErrMemberLinkNotFound) || errors.Is(err, ErrIneligible) {
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
	request *DeletionRequest,
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
	if errors.Is(err, ErrDeletionRequestNotFound) {
		return domainerr.New(domainerr.NotFound, "", "invalid or expired token").WithCause(err)
	}
	return err
}

func (s *Service) deletionRequestIdentityIDForToken(
	ctx context.Context,
	tokenHash string,
) (string, error) {
	var identityID string
	err := s.read(ctx, func(store Store) error {
		var err error
		identityID, err = store.DeletionRequestIdentityByToken(ctx, tokenHash)
		return err
	})
	return identityID, err
}

// tokenTransition moves the deletion request an emailed token names from
// one of from to its next state. The token is the caller's authority: the
// request is read under its Identity fence, and a missing or expired token
// answers before anything changes.
type tokenTransition struct {
	token string
	from  []string
	// apply changes the locked request and reports whether it changed.
	apply func(ctx context.Context, store Store, request *DeletionRequest) (bool, error)
	audit func(memberID string) mutation.Audit
}

type tokenTransitioned struct {
	request DeletionRequest
	changed bool
}

func (s *Service) transitionByToken(ctx context.Context, transition tokenTransition, now time.Time) (DeletionRequest, error) {
	token := strings.TrimSpace(transition.token)
	if token == "" {
		return DeletionRequest{}, domainerr.Required("token")
	}
	tokenHash := crypto.HashToken(token)
	identityID, err := s.deletionRequestIdentityIDForToken(ctx, tokenHash)
	if err != nil {
		return DeletionRequest{}, accountLifecycleTokenError(err)
	}
	var request DeletionRequest
	result, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[tokenTransitioned, Store]{
		Lock: func(ctx context.Context, store Store) error { return store.LockIdentity(ctx, identityID) },
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			var err error
			request, err = store.LockDeletionRequestByToken(ctx, tokenHash, now, transition.from)
			return err
		}),
		Change: func(ctx context.Context, store Store) (tokenTransitioned, error) {
			changed, err := transition.apply(ctx, store, &request)
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
			return DeletionRequest{}, domainerr.Precondition("the last active admin cannot be deleted")
		}
		return DeletionRequest{}, accountLifecycleTokenError(err)
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
	job := Email{
		Recipient:    recipient,
		TemplateType: emailpkg.EventAccountDeletionScheduled.String(),
		TemplateData: map[string]string{
			"name":           name,
			"scheduled_date": request.ScheduledAt.Format("2006-01-02"),
			"grace_period":   "30 days",
			"cancel_url":     fmt.Sprintf("%s/account/deletion/cancel?token=%s", s.baseURL, strings.TrimSpace(command.Token)),
			"recover_url":    s.baseURL + "/account/recover",
		},
		SystemReason: "account_deletion_scheduled",
		ReferenceID:  &request.ID,
		Locale:       request.NotificationLocale,
		MessageID:    "account-deletion-scheduled:" + request.ID,
	}
	if err := s.emails.PublishEmail(ctx, job); err != nil {
		return AccountDeletionScheduledResult{}, emailPublishFailure(err, job, strings.TrimSpace(command.Token))
	}
	return AccountDeletionScheduledResult{ScheduledAt: *request.ScheduledAt}, nil
}

// scheduleConfirmedDeletion schedules a confirmation-pending request for the
// end of the grace period; a request already scheduled is unchanged. The
// last-admin fence keeps the last active admin from confirming.
func (s *Service) scheduleConfirmedDeletion(now time.Time) func(context.Context, Store, *DeletionRequest) (bool, error) {
	return func(ctx context.Context, store Store, request *DeletionRequest) (bool, error) {
		if request.LifecycleState == accountLifecycleStateScheduled {
			return false, nil
		}
		if err := validateLastActiveAdminDeletion(ctx, store, s.spicedb, request.MemberID, request.IdentityID); err != nil {
			return false, err
		}
		scheduledAt := now.Add(DeletionGracePeriod)
		if err := store.ScheduleDeletionRequest(ctx, request.ID, now, scheduledAt); err != nil {
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
	job := Email{
		Recipient:         recipient,
		TemplateType:      emailpkg.EventAccountDeletionCancelled.String(),
		TemplateData:      map[string]string{"name": name, "login_url": s.baseURL + "/login"},
		AccountIdentityID: request.IdentityID,
		ReferenceID:       &request.ID,
		Locale:            request.NotificationLocale,
		MessageID:         "account-deletion-cancelled:" + request.ID,
	}
	if err := s.emails.PublishEmail(ctx, job); err != nil {
		return emailPublishFailure(err, job, strings.TrimSpace(command.Token))
	}
	return nil
}

// terminalTransition ends a deletion request in state, clearing its
// deadline; a request already there is unchanged.
func terminalTransition(state string) func(context.Context, Store, *DeletionRequest) (bool, error) {
	return func(ctx context.Context, store Store, request *DeletionRequest) (bool, error) {
		if request.LifecycleState == state {
			return false, nil
		}
		if err := store.EndDeletionRequest(ctx, request.ID, state); err != nil {
			return false, err
		}
		request.ScheduledAt = nil
		request.LifecycleState = state
		return true, nil
	}
}
