package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/structured"
)

var ErrAccountEmailCandidateNotProven = errors.New("account email candidate is not proven")

// ApplyProvenCandidate selects an existing authoritative candidate without a
// new email-code proof. It still records the active request before changing
// Kratos so notification handoff can recover after a crash.
func (s *AccountEmailChangeLifecycle) ApplyProvenCandidate(
	ctx context.Context,
	identityID string,
	targetEmail string,
) error {
	identityID = strings.TrimSpace(identityID)
	targetEmail = normalizeAccountEmail(targetEmail)
	if identityID == "" || targetEmail == "" {
		return fmt.Errorf("identity and target email are required")
	}
	return s.units.Fenced(ctx, identityID, func(mutationCtx context.Context, steps Steps) error {
		return s.applyProvenCandidateMutation(mutationCtx, steps, identityID, targetEmail)
	})
}

type provenAccountEmailCandidate struct {
	identity      *auth.Identity
	memberID      string
	previousEmail string
	displayEmail  string
}

type provenAccountEmailStage struct {
	request  EmailChangeRequest
	noChange bool
}

func (s *AccountEmailChangeLifecycle) applyProvenCandidateMutation(
	ctx context.Context,
	steps Steps,
	identityID string,
	targetEmail string,
) error {
	candidate, err := s.loadProvenAccountEmailCandidate(ctx, steps, identityID, targetEmail)
	if err != nil {
		return err
	}
	stage, err := s.stageProvenAccountEmailCandidate(ctx, steps, identityID, targetEmail, candidate)
	if err != nil || stage.noChange {
		return err
	}
	if accountEmailsEqual(candidate.previousEmail, targetEmail) {
		return s.completeAccountEmailChangeRequest(ctx, steps, &stage.request)
	}
	if err := s.applyProvenAccountEmailIdentity(ctx, steps, identityID, candidate, &stage.request); err != nil {
		return err
	}
	return s.completeAccountEmailChangeRequest(ctx, steps, &stage.request)
}

func (s *AccountEmailChangeLifecycle) loadProvenAccountEmailCandidate(
	ctx context.Context,
	steps Steps,
	identityID string,
	targetEmail string,
) (provenAccountEmailCandidate, error) {
	identity, err := LoadIdentityWithEmailCredentials(ctx, s.identity, identityID)
	if err != nil {
		return provenAccountEmailCandidate{}, err
	}
	if identity == nil {
		return provenAccountEmailCandidate{}, ErrIdentityNotFound
	}
	var memberID string
	if err := steps.Step(ctx, func(store Store) error {
		var err error
		memberID, err = store.ActiveMemberIDForIdentity(ctx, identityID)
		return err
	}); err != nil {
		return provenAccountEmailCandidate{}, fmt.Errorf("resolve account email change member: %w", err)
	}
	providerCandidates := ResolveAccountEmailProviderCandidates(ctx, identity.Credentials)
	rows, err := newAccountEmailService(s.identity, steps.Step).
		SyncMemberEmailProjection(ctx, identityID, identity, providerCandidates)
	if err != nil {
		return provenAccountEmailCandidate{}, err
	}
	selected := findProjectionRow(rows, targetEmail)
	if selected == nil || !selected.UsableForDelivery {
		return provenAccountEmailCandidate{}, ErrAccountEmailCandidateNotProven
	}
	previousEmail := normalizeAccountEmail(identity.CurrentEmail())
	if previousEmail == "" {
		return provenAccountEmailCandidate{}, fmt.Errorf("canonical account email is required")
	}
	return provenAccountEmailCandidate{
		identity: identity, memberID: memberID,
		previousEmail: previousEmail, displayEmail: selected.DisplayEmail,
	}, nil
}

func (s *AccountEmailChangeLifecycle) stageProvenAccountEmailCandidate(
	ctx context.Context,
	steps Steps,
	identityID string,
	targetEmail string,
	candidate provenAccountEmailCandidate,
) (provenAccountEmailStage, error) {
	stage := provenAccountEmailStage{}
	err := steps.Step(ctx, func(store Store) error {
		active, err := store.LockEmailChangeRequest(ctx, identityID)
		if err != nil {
			return err
		}
		if active != nil {
			stage.request = *active
		}
		if accountEmailsEqual(candidate.previousEmail, targetEmail) {
			return reconcileProvenCandidateNoChange(ctx, store, candidate.identity, targetEmail, &stage)
		}
		keep, err := reconcileProvenCandidateActiveRequest(
			ctx, store, candidate.identity, candidate.previousEmail, targetEmail, &stage.request,
		)
		if err != nil || keep {
			return err
		}
		stage.request = EmailChangeRequest{
			ID: uuid.NewString(), MemberID: candidate.memberID, IdentityID: identityID,
			PreviousEmailAddress: candidate.previousEmail, RequestedEmailAddress: targetEmail,
			CreatedAt: s.now().UTC(),
		}
		if err := store.InsertEmailChangeRequest(ctx, stage.request); errors.Is(err, ErrEmailChangeRequestExists) {
			return fmt.Errorf("%w: %w", ErrAccountEmailChangeConflict, err)
		} else {
			return err
		}
	})
	return stage, err
}

func reconcileProvenCandidateNoChange(
	ctx context.Context,
	store Store,
	identity *auth.Identity,
	targetEmail string,
	stage *provenAccountEmailStage,
) error {
	if stage.request.ID == "" {
		stage.noChange = true
		return nil
	}
	if accountEmailsEqual(stage.request.RequestedEmailAddress, targetEmail) {
		return nil
	}
	if identity.HasVerifiedEmailAddress(stage.request.RequestedEmailAddress) {
		return ErrAccountEmailChangeInFlight
	}
	if err := store.DeleteEmailChangeRequest(ctx, stage.request.ID); err != nil {
		return err
	}
	stage.request = EmailChangeRequest{}
	stage.noChange = true
	return nil
}

func reconcileProvenCandidateActiveRequest(
	ctx context.Context,
	store Store,
	identity *auth.Identity,
	previousEmail string,
	targetEmail string,
	active *EmailChangeRequest,
) (bool, error) {
	if active.ID == "" {
		return false, nil
	}
	if accountEmailsEqual(active.PreviousEmailAddress, previousEmail) &&
		accountEmailsEqual(active.RequestedEmailAddress, targetEmail) {
		return true, nil
	}
	if identity.HasVerifiedEmailAddress(active.RequestedEmailAddress) {
		return false, ErrAccountEmailChangeInFlight
	}
	if err := store.DeleteEmailChangeRequest(ctx, active.ID); err != nil {
		return false, err
	}
	return false, nil
}

func (s *AccountEmailChangeLifecycle) applyProvenAccountEmailIdentity(
	ctx context.Context,
	steps Steps,
	identityID string,
	candidate provenAccountEmailCandidate,
	request *EmailChangeRequest,
) error {
	nextAddresses := materializeProvenEmailAddress(
		candidate.identity.VerifiableAddresses, candidate.displayEmail, s.now().UTC(),
	)
	currentEmail := candidate.displayEmail
	err := s.identity.UpdateIdentityAccountEmailState(
		ctx, identityID, &currentEmail, structured.Fields{"pending_email": nil}, nextAddresses,
	)
	if !auth.IsKratosConflict(err) {
		return err
	}
	if finishErr := s.finishAccountEmailChangeRequest(ctx, steps, request, "address_conflict"); finishErr != nil {
		return finishErr
	}
	// The text keeps the Kratos conflict; its body stays private.
	return failurecause.WithPrivate(
		fmt.Errorf("%w: %v", ErrAccountEmailChangeConflict, err),
		failurecause.PrivateValues(err)...,
	)
}

// materializeProvenEmailAddress projects an already-authoritative proof into
// Kratos' verifiable-address state. The status literal is Ory's wire value.
func materializeProvenEmailAddress(
	addresses []auth.VerifiableAddress,
	email string,
	verifiedAt time.Time,
) []auth.VerifiableAddress {
	normalizedEmail := emailutil.NormalizeAddressForDelivery(email)
	next := append([]auth.VerifiableAddress(nil), addresses...)
	for i := range next {
		if next[i].Via != "email" || emailutil.NormalizeAddressForDelivery(next[i].Value) != normalizedEmail {
			continue
		}
		next[i].Verified = true
		next[i].Status = "completed"
		next[i].VerifiedAt = &verifiedAt
		return next
	}

	next = append(next, auth.VerifiableAddress{
		Value:      normalizedEmail,
		Via:        "email",
		Verified:   true,
		Status:     "completed",
		VerifiedAt: &verifiedAt,
	})
	return next
}
