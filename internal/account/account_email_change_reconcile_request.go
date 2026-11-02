package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/structured"
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

	return s.units.Fenced(ctx, identityID, func(mutationCtx context.Context, steps Steps) error {
		var request *EmailChangeRequest
		if err := steps.Step(mutationCtx, func(store Store) error {
			var err error
			request, err = store.EmailChangeRequestFor(mutationCtx, identityID, previousEmail, requestedEmail)
			return err
		}); err != nil {
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
		return s.reconcileAccountEmailChangeRequest(mutationCtx, steps, request)
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

	var request *EmailChangeRequest
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		request, err = store.EmailChangeRequest(ctx, requestID)
		return err
	}); err != nil || request == nil {
		return err
	}
	return s.units.Fenced(ctx, request.IdentityID, func(mutationCtx context.Context, steps Steps) error {
		var current *EmailChangeRequest
		if err := steps.Step(mutationCtx, func(store Store) error {
			var err error
			current, err = store.EmailChangeRequest(mutationCtx, requestID)
			return err
		}); err != nil || current == nil {
			return err
		}
		return s.reconcileAccountEmailChangeRequest(mutationCtx, steps, current)
	})
}

func (s *AccountEmailChangeLifecycle) reconcileAccountEmailChangeRequest(
	ctx context.Context,
	steps Steps,
	request *EmailChangeRequest,
) error {
	identity, err := s.identity.GetIdentity(ctx, request.IdentityID)
	if err != nil {
		return err
	}
	if identity == nil {
		return s.finishAccountEmailChangeRequest(ctx, steps, request, "invalid_identity")
	}

	currentEmail := normalizeAccountEmail(identity.CurrentEmail())
	pendingEmail := normalizeAccountEmail(identity.PendingEmail())
	previousEmail := request.PreviousEmailAddress
	requestedEmail := request.RequestedEmailAddress

	if accountEmailsEqual(currentEmail, requestedEmail) {
		return s.reconcileCanonicalRequestedAccountEmail(
			ctx, steps, request, identity, pendingEmail,
		)
	}

	if !accountEmailsEqual(currentEmail, previousEmail) {
		if err := s.clearRequestedAddress(ctx, request, identity); err != nil {
			return err
		}
		return s.finishAccountEmailChangeRequest(ctx, steps, request, "canonical_conflict")
	}

	if accountEmailsEqual(pendingEmail, requestedEmail) &&
		identity.HasVerifiedEmailAddress(requestedEmail) {
		return s.reconcileVerifiedPendingAccountEmail(ctx, steps, request, identity)
	}

	// An exact pending address is still an active user-visible request even when
	// its previous verification code expired. Kratos owns code expiry and the
	// user may restart proof without losing the request, until the pending
	// lifetime ends and the address is released for other accounts.
	if accountEmailsEqual(pendingEmail, requestedEmail) {
		if s.now().UTC().Sub(request.CreatedAt) < accountEmailChangePendingLifetime {
			return nil
		}
		if err := s.clearRequestedAddress(ctx, request, identity); err != nil {
			return err
		}
		return s.finishAccountEmailChangeRequest(ctx, steps, request, "expired")
	}
	if s.now().UTC().Sub(request.CreatedAt) < accountEmailChangeProofGrace {
		return nil
	}
	if err := s.clearRequestedAddress(ctx, request, identity); err != nil {
		return err
	}
	return s.finishAccountEmailChangeRequest(ctx, steps, request, "abandoned")
}

func (s *AccountEmailChangeLifecycle) reconcileCanonicalRequestedAccountEmail(
	ctx context.Context,
	steps Steps,
	request *EmailChangeRequest,
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
		return s.completeAccountEmailChangeRequest(ctx, steps, request)
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
	return s.completeAccountEmailChangeRequest(ctx, steps, request)
}

func (s *AccountEmailChangeLifecycle) reconcileVerifiedPendingAccountEmail(
	ctx context.Context,
	steps Steps,
	request *EmailChangeRequest,
	identity *auth.Identity,
) error {
	used, err := emailCodeAddressUsedByAnotherIdentity(
		ctx, s.identity, request.IdentityID, request.RequestedEmailAddress,
	)
	if err != nil {
		return err
	}
	if used {
		return s.rejectConflictingAccountEmail(ctx, steps, request, identity)
	}
	if err := s.applyAccountEmailChangeRequest(ctx, request, identity); err != nil {
		return s.resolveAccountEmailApplyError(ctx, steps, request, err)
	}
	return s.completeAccountEmailChangeRequest(ctx, steps, request)
}

func (s *AccountEmailChangeLifecycle) rejectConflictingAccountEmail(
	ctx context.Context,
	steps Steps,
	request *EmailChangeRequest,
	identity *auth.Identity,
) error {
	if err := s.clearConflictingAccountEmailRequest(ctx, steps, request, identity); err != nil {
		return err
	}
	return ErrAccountEmailChangeConflict
}

func (s *AccountEmailChangeLifecycle) resolveAccountEmailApplyError(
	ctx context.Context,
	steps Steps,
	request *EmailChangeRequest,
	applyErr error,
) error {
	if !auth.IsKratosConflict(applyErr) {
		return applyErr
	}
	current, err := s.identity.GetIdentity(ctx, request.IdentityID)
	if err != nil {
		return err
	}
	if err := s.clearConflictingAccountEmailRequest(ctx, steps, request, current); err != nil {
		return err
	}
	// The text keeps the Kratos conflict; its body stays private.
	return failurecause.WithPrivate(
		fmt.Errorf("%w: %v", ErrAccountEmailChangeConflict, applyErr),
		failurecause.PrivateValues(applyErr)...,
	)
}

func (s *AccountEmailChangeLifecycle) applyAccountEmailChangeRequest(
	ctx context.Context,
	request *EmailChangeRequest,
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
	steps Steps,
	request *EmailChangeRequest,
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
	// The projection syncs on the pool in a transaction of its own, outside
	// the fenced connection.
	if _, err := newAccountEmailService(s.identity, s.transact).
		SyncMemberEmailProjection(ctx, request.IdentityID, mergedIdentity, providerCandidates); err != nil {
		return fmt.Errorf("sync account email projection: %w", err)
	}

	referenceID := request.ID
	if err := s.publisher.PublishEmail(ctx, Email{
		Recipient:    request.PreviousEmailAddress,
		TemplateType: email.EventPrimaryEmailChanged.String(),
		TemplateData: map[string]string{
			"old_email": request.PreviousEmailAddress,
			"new_email": request.RequestedEmailAddress,
		},
		ReferenceID:  &referenceID,
		MessageID:    "account-email-change:" + request.ID,
		SystemReason: email.EventPrimaryEmailChanged.String(),
	}); err != nil {
		return fmt.Errorf("%w: %v", ErrAccountEmailChangeNotificationPublish, err)
	}
	return s.finishAccountEmailChangeRequest(ctx, steps, request, "changed")
}

func (s *AccountEmailChangeLifecycle) clearConflictingAccountEmailRequest(
	ctx context.Context,
	steps Steps,
	request *EmailChangeRequest,
	identity *auth.Identity,
) error {
	if err := s.clearRequestedAddress(ctx, request, identity); err != nil {
		return err
	}
	return s.finishAccountEmailChangeRequest(ctx, steps, request, "address_conflict")
}

func (s *AccountEmailChangeLifecycle) clearRequestedAddress(
	ctx context.Context,
	request *EmailChangeRequest,
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
	ctx context.Context,
	steps Steps,
	request *EmailChangeRequest,
	reason string,
) error {
	if err := steps.Step(ctx, func(store Store) error {
		return store.DeleteEmailChangeRequest(ctx, request.ID)
	}); err != nil {
		if errors.Is(err, ErrEmailChangeRequestNotFound) {
			return nil
		}
		return err
	}
	slog.InfoContext(ctx,
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
