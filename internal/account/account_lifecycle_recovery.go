package account

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	emailpkg "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
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
	job := Email{
		Recipient:    requestedEmail,
		TemplateType: emailpkg.EventAccountRecoveryConfirm.String(),
		TemplateData: map[string]string{
			"name":        name,
			"confirm_url": fmt.Sprintf("%s/account/recovery/confirm?token=%s", s.baseURL, token),
			"expires_in":  "24 hours",
		},
		SystemReason: "account_recovery_confirm",
		ReferenceID:  &tokenHash,
		Locale:       request.NotificationLocale,
		MessageID:    "account-recovery-confirm:" + tokenHash,
	}
	if err := s.emails.PublishEmail(ctx, job); err != nil {
		return emailPublishFailure(err, job, token)
	}
	return nil
}

// startRecovery moves the pending deletion recorded for requestedEmail to
// recovery confirmation under tokenHash. It returns the zero request when no
// deletion of that address is pending.
func (s *Service) startRecovery(ctx context.Context, requestedEmail, tokenHash string) (DeletionRequest, error) {
	now := time.Now().UTC()
	var (
		candidateIdentityID string
		found               bool
	)
	if err := s.read(ctx, func(store Store) error {
		var err error
		candidateIdentityID, found, err = store.PendingRecoveryIdentity(ctx, requestedEmail, now)
		return err
	}); err != nil || !found {
		return DeletionRequest{}, err
	}
	var request DeletionRequest
	if _, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Lock: func(ctx context.Context, store Store) error { return store.LockIdentity(ctx, candidateIdentityID) },
		// The recovery link goes only to the address the deletion recorded,
		// so the address itself is the request's authority.
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			current, found, err := store.LockPendingRecovery(ctx, requestedEmail, now)
			if found {
				request = current
			}
			return err
		}),
		Change: func(ctx context.Context, store Store) (struct{}, error) {
			if request.ID == "" {
				return struct{}{}, nil
			}
			return struct{}{}, store.StartRecovery(ctx, request.ID, tokenHash, now.Add(DeletionTokenExpiry), now)
		},
		// Only a confirmed recovery is a lifecycle transition worth a record.
		Audit: func(struct{}) mutation.Audit { return mutation.Unchanged() },
	}); err != nil {
		return DeletionRequest{}, err
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
	job := Email{
		Recipient:    recipient,
		TemplateType: emailpkg.EventAccountRecoveryComplete.String(),
		TemplateData: map[string]string{"name": name, "login_url": s.baseURL + "/login"},
		SystemReason: "account_recovery_complete",
		ReferenceID:  &request.ID,
		Locale:       request.NotificationLocale,
		MessageID:    "account-recovery-complete:" + request.ID,
	}
	if err := s.emails.PublishEmail(ctx, job); err != nil {
		return emailPublishFailure(err, job, strings.TrimSpace(command.Token))
	}
	return nil
}
