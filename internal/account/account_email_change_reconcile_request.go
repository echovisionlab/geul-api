package account

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// VerifyAndReconcile runs after Kratos has persisted verification. The flow ID
// is validated as hook input but is not a durable authority and is not stored.
func (s *AccountEmailChangeLifecycle) VerifyAndReconcile(
	ctx context.Context,
	verificationFlowID string,
	identityID string,
	previousEmail string,
	requestedEmail string,
) error {
	if strings.TrimSpace(verificationFlowID) == "" {
		return fmt.Errorf("verification flow id is required")
	}
	identityID = strings.TrimSpace(identityID)
	previousEmail = normalizeAccountEmail(previousEmail)
	requestedEmail = normalizeAccountEmail(requestedEmail)
	if identityID == "" || previousEmail == "" || requestedEmail == "" {
		return fmt.Errorf("identity, previous email, and requested email are required")
	}

	return identitystate.WithMutation(ctx, s.db, identityID, func(
		mutationCtx context.Context,
		connection *gorm.DB,
	) error {
		request, err := loadAccountEmailChangeRequest(
			connection,
			identityID,
			previousEmail,
			requestedEmail,
		)
		if err != nil {
			return err
		}
		if request == nil {
			identity, err := s.identity.GetIdentity(mutationCtx, identityID)
			if err != nil {
				return err
			}
			if identity != nil &&
				accountEmailsEqual(identity.CurrentEmail(), requestedEmail) &&
				identity.HasVerifiedEmailAddress(requestedEmail) {
				return nil
			}
			return fmt.Errorf("verified account email was not durably staged")
		}
		return s.reconcileAccountEmailChangeRequest(mutationCtx, connection, request)
	})
}

func (s *AccountEmailChangeLifecycle) ReconcileRequest(
	ctx context.Context,
	requestID string,
) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return fmt.Errorf("account email change request id is required")
	}

	var request model.AccountEmailChangeRequest
	if err := s.db.WithContext(ctx).First(&request, "id = ?", requestID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		return err
	}
	return identitystate.WithMutation(ctx, s.db, request.IdentityID, func(
		mutationCtx context.Context,
		connection *gorm.DB,
	) error {
		var current model.AccountEmailChangeRequest
		if err := connection.First(&current, "id = ?", requestID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return err
		}
		return s.reconcileAccountEmailChangeRequest(mutationCtx, connection, &current)
	})
}

func (s *AccountEmailChangeLifecycle) reconcileAccountEmailChangeRequest(
	ctx context.Context,
	db *gorm.DB,
	request *model.AccountEmailChangeRequest,
) error {
	identity, err := s.identity.GetIdentity(ctx, request.IdentityID)
	if err != nil {
		return err
	}
	if identity == nil {
		return s.finishAccountEmailChangeRequest(db, request, "invalid_identity")
	}

	currentEmail := normalizeAccountEmail(identity.CurrentEmail())
	pendingEmail := normalizeAccountEmail(identity.PendingEmail())
	previousEmail := request.PreviousEmailAddress
	requestedEmail := request.RequestedEmailAddress

	if accountEmailsEqual(currentEmail, requestedEmail) {
		return s.reconcileCanonicalRequestedAccountEmail(
			ctx, db, request, identity, pendingEmail,
		)
	}

	if !accountEmailsEqual(currentEmail, previousEmail) {
		if err := s.clearRequestedAddress(ctx, request, identity); err != nil {
			return err
		}
		return s.finishAccountEmailChangeRequest(db, request, "canonical_conflict")
	}

	if accountEmailsEqual(pendingEmail, requestedEmail) &&
		identity.HasVerifiedEmailAddress(requestedEmail) {
		return s.reconcileVerifiedPendingAccountEmail(ctx, db, request, identity)
	}

	// An exact pending address is still an active user-visible request even when
	// its previous verification code expired. Kratos owns code expiry and the
	// user may restart proof without losing the request.
	if accountEmailsEqual(pendingEmail, requestedEmail) {
		return nil
	}
	if s.now().UTC().Sub(request.CreatedAt) < accountEmailChangeProofGrace {
		return nil
	}
	if err := s.clearRequestedAddress(ctx, request, identity); err != nil {
		return err
	}
	return s.finishAccountEmailChangeRequest(db, request, "abandoned")
}

func (s *AccountEmailChangeLifecycle) reconcileCanonicalRequestedAccountEmail(
	ctx context.Context,
	db *gorm.DB,
	request *model.AccountEmailChangeRequest,
	identity *auth.Identity,
	pendingEmail string,
) error {
	requestedEmail := request.RequestedEmailAddress
	if !identity.HasVerifiedEmailAddress(requestedEmail) {
		return fmt.Errorf("canonical requested email is not verified")
	}
	if pendingEmail != "" && !accountEmailsEqual(pendingEmail, requestedEmail) {
		return fmt.Errorf("canonical requested email has another pending address")
	}
	if !accountEmailsEqual(pendingEmail, requestedEmail) {
		return s.completeAccountEmailChangeRequest(ctx, db, request)
	}
	if err := s.identity.UpdateIdentityAccountEmailState(
		ctx,
		request.IdentityID,
		nil,
		structured.Fields{"pending_email": nil},
		nil,
	); err != nil {
		return err
	}
	return s.completeAccountEmailChangeRequest(ctx, db, request)
}

func (s *AccountEmailChangeLifecycle) reconcileVerifiedPendingAccountEmail(
	ctx context.Context,
	db *gorm.DB,
	request *model.AccountEmailChangeRequest,
	identity *auth.Identity,
) error {
	used, err := emailCodeAddressUsedByAnotherIdentity(
		ctx, s.identity, request.IdentityID, request.RequestedEmailAddress,
	)
	if err != nil {
		return err
	}
	if used {
		return s.rejectConflictingAccountEmail(ctx, db, request, identity)
	}
	if err := s.applyAccountEmailChangeRequest(ctx, request, identity); err != nil {
		return s.resolveAccountEmailApplyError(ctx, db, request, err)
	}
	return s.completeAccountEmailChangeRequest(ctx, db, request)
}

func (s *AccountEmailChangeLifecycle) rejectConflictingAccountEmail(
	ctx context.Context,
	db *gorm.DB,
	request *model.AccountEmailChangeRequest,
	identity *auth.Identity,
) error {
	if err := s.clearConflictingAccountEmailRequest(ctx, db, request, identity); err != nil {
		return err
	}
	return ErrAccountEmailChangeConflict
}

func (s *AccountEmailChangeLifecycle) resolveAccountEmailApplyError(
	ctx context.Context,
	db *gorm.DB,
	request *model.AccountEmailChangeRequest,
	applyErr error,
) error {
	if !auth.IsKratosConflict(applyErr) {
		return applyErr
	}
	current, err := s.identity.GetIdentity(ctx, request.IdentityID)
	if err != nil {
		return err
	}
	if err := s.clearConflictingAccountEmailRequest(ctx, db, request, current); err != nil {
		return err
	}
	return fmt.Errorf("%w: %v", ErrAccountEmailChangeConflict, applyErr)
}

func (s *AccountEmailChangeLifecycle) applyAccountEmailChangeRequest(
	ctx context.Context,
	request *model.AccountEmailChangeRequest,
	identity *auth.Identity,
) error {
	if !identity.HasVerifiedEmailAddress(request.RequestedEmailAddress) {
		return fmt.Errorf("verified requested email address is missing")
	}
	requestedEmail := request.RequestedEmailAddress
	return s.identity.UpdateIdentityAccountEmailState(
		ctx,
		request.IdentityID,
		&requestedEmail,
		structured.Fields{"pending_email": nil},
		nil,
	)
}

func (s *AccountEmailChangeLifecycle) completeAccountEmailChangeRequest(
	ctx context.Context,
	db *gorm.DB,
	request *model.AccountEmailChangeRequest,
) error {
	mergedIdentity, err := LoadIdentityWithEmailCredentials(ctx, s.identity, request.IdentityID)
	if err != nil {
		return err
	}
	if mergedIdentity == nil ||
		!accountEmailsEqual(mergedIdentity.CurrentEmail(), request.RequestedEmailAddress) ||
		mergedIdentity.PendingEmail() != "" ||
		!mergedIdentity.HasVerifiedEmailAddress(request.RequestedEmailAddress) {
		return fmt.Errorf("account email identity did not converge")
	}
	providerCandidates := ResolveAccountEmailProviderCandidates(ctx, mergedIdentity.Credentials)
	if _, err := NewAccountEmailService(s.db, s.identity, s.memberEmails).
		SyncMemberEmailProjection(ctx, request.IdentityID, mergedIdentity, providerCandidates); err != nil {
		return fmt.Errorf("sync account email projection: %w", err)
	}

	referenceID := request.ID
	messageID := "account-email-change:" + request.ID
	if err := s.publisher.PublishSendEmail(ctx, &managev1.SendEmailEvent{
		Recipient:    request.PreviousEmailAddress,
		TemplateType: email.EventPrimaryEmailChanged.String(),
		TemplateData: map[string]string{
			"old_email": request.PreviousEmailAddress,
			"new_email": request.RequestedEmailAddress,
		},
		ReferenceId: &referenceID,
		MessageId:   &messageID,
		RecipientContext: email.SystemDirectEmailContext(
			email.EventPrimaryEmailChanged.String(),
		),
	}); err != nil {
		return fmt.Errorf("%w: %v", ErrAccountEmailChangeNotificationPublish, err)
	}
	return s.finishAccountEmailChangeRequest(db, request, "changed")
}

func (s *AccountEmailChangeLifecycle) clearConflictingAccountEmailRequest(
	ctx context.Context,
	db *gorm.DB,
	request *model.AccountEmailChangeRequest,
	identity *auth.Identity,
) error {
	if err := s.clearRequestedAddress(ctx, request, identity); err != nil {
		return err
	}
	return s.finishAccountEmailChangeRequest(db, request, "address_conflict")
}

func (s *AccountEmailChangeLifecycle) clearRequestedAddress(
	ctx context.Context,
	request *model.AccountEmailChangeRequest,
	identity *auth.Identity,
) error {
	if identity == nil || accountEmailsEqual(identity.CurrentEmail(), request.RequestedEmailAddress) {
		return nil
	}
	traits := structured.Fields{}
	if accountEmailsEqual(identity.PendingEmail(), request.RequestedEmailAddress) {
		traits["pending_email"] = nil
	}
	addresses := make([]auth.VerifiableAddress, 0, len(identity.VerifiableAddresses))
	removedAddress := false
	for _, address := range identity.VerifiableAddresses {
		if strings.EqualFold(strings.TrimSpace(address.Via), "email") &&
			accountEmailsEqual(address.Value, request.RequestedEmailAddress) {
			removedAddress = true
			continue
		}
		addresses = append(addresses, address)
	}
	if len(traits) == 0 && !removedAddress {
		return nil
	}
	if !removedAddress {
		addresses = nil
	}
	return s.identity.UpdateIdentityAccountEmailState(
		ctx,
		request.IdentityID,
		nil,
		traits,
		addresses,
	)
}

func (s *AccountEmailChangeLifecycle) finishAccountEmailChangeRequest(
	db *gorm.DB,
	request *model.AccountEmailChangeRequest,
	reason string,
) error {
	if err := deleteAccountEmailChangeRequest(db, request.ID); err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		return err
	}
	slog.Info(
		"Account email change request finished",
		"domain", "auth",
		"event", "auth.account_email_change.finished",
		"outcome", reason,
		"reason", reason,
		"request_id", request.ID,
		"identity_id", request.IdentityID,
	)
	return nil
}

func loadAccountEmailChangeRequest(
	db *gorm.DB,
	identityID string,
	previousEmail string,
	requestedEmail string,
) (*model.AccountEmailChangeRequest, error) {
	var request model.AccountEmailChangeRequest
	err := db.Where(
		"identity_id = ?::uuid AND previous_email_address = ? AND requested_email_address = ?",
		identityID,
		normalizeAccountEmail(previousEmail),
		normalizeAccountEmail(requestedEmail),
	).First(&request).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &request, nil
}
