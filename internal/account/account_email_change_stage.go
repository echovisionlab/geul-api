package account

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
)

type accountEmailChangeStage struct {
	identityID            string
	currentEmail          string
	currentPendingEmail   string
	candidatePendingEmail string
	pendingVerified       bool
}

// StageOrCancel runs from Kratos' parsed pre-persist settings hook. The active
// request must be committed before Kratos may persist pending_email. Flow IDs
// are deliberately not persisted because Kratos state is the proof authority.
func (s *AccountEmailChangeLifecycle) StageOrCancel(
	ctx context.Context,
	settingsFlowID string,
	identityID string,
	currentEmail string,
	currentPendingEmail string,
	candidatePendingEmail string,
	pendingVerified bool,
) error {
	stage, err := newAccountEmailChangeStage(
		settingsFlowID,
		identityID,
		currentEmail,
		currentPendingEmail,
		candidatePendingEmail,
		pendingVerified,
	)
	if err != nil {
		return err
	}

	return s.units.Fenced(ctx, stage.identityID, func(mutationCtx context.Context, steps Steps) error {
		identity, memberID, err := s.loadAccountEmailChangeStageState(mutationCtx, steps, stage)
		if err != nil {
			return err
		}
		return steps.Step(mutationCtx, func(store Store) error {
			return s.persistAccountEmailChangeStage(mutationCtx, store, stage, identity, memberID)
		})
	})
}

func newAccountEmailChangeStage(
	settingsFlowID string,
	identityID string,
	currentEmail string,
	currentPendingEmail string,
	candidatePendingEmail string,
	pendingVerified bool,
) (accountEmailChangeStage, error) {
	if strings.TrimSpace(settingsFlowID) == "" {
		return accountEmailChangeStage{}, fmt.Errorf("settings flow id is required")
	}
	stage := accountEmailChangeStage{
		identityID:            strings.TrimSpace(identityID),
		currentEmail:          normalizeAccountEmail(currentEmail),
		currentPendingEmail:   normalizeAccountEmail(currentPendingEmail),
		candidatePendingEmail: normalizeAccountEmail(candidatePendingEmail),
		pendingVerified:       pendingVerified,
	}
	if stage.identityID == "" || stage.currentEmail == "" {
		return accountEmailChangeStage{}, fmt.Errorf("identity and current email are required")
	}
	if err := validateAccountEmailLength(stage.currentEmail); err != nil {
		return accountEmailChangeStage{}, err
	}
	if err := validateAccountEmailLength(stage.candidatePendingEmail); err != nil {
		return accountEmailChangeStage{}, err
	}
	return stage, nil
}

func (s *AccountEmailChangeLifecycle) loadAccountEmailChangeStageState(
	ctx context.Context,
	steps Steps,
	stage accountEmailChangeStage,
) (*auth.Identity, string, error) {
	identity, err := s.identity.GetIdentity(ctx, stage.identityID)
	if err != nil {
		return nil, "", fmt.Errorf("load identity before account email change: %w", err)
	}
	if identity == nil || !accountEmailsEqual(identity.CurrentEmail(), stage.currentEmail) {
		return nil, "", fmt.Errorf("canonical identity email changed before account email request")
	}
	if !accountEmailsEqual(identity.PendingEmail(), stage.currentPendingEmail) {
		return nil, "", fmt.Errorf("pending identity email changed before account email request")
	}
	var memberID string
	if err := steps.Step(ctx, func(store Store) error {
		var err error
		memberID, err = store.ActiveMemberIDForIdentity(ctx, stage.identityID)
		return err
	}); err != nil {
		return nil, "", fmt.Errorf("resolve account email change member: %w", err)
	}
	return identity, memberID, nil
}

func (s *AccountEmailChangeLifecycle) persistAccountEmailChangeStage(
	ctx context.Context,
	store Store,
	stage accountEmailChangeStage,
	identity *auth.Identity,
	memberID string,
) error {
	active, err := store.LockEmailChangeRequest(ctx, stage.identityID)
	if err != nil {
		return err
	}
	if stage.currentPendingEmail != "" {
		stage.pendingVerified = identity.HasVerifiedEmailAddress(stage.currentPendingEmail)
	}
	if stage.candidatePendingEmail == "" || accountEmailsEqual(stage.candidatePendingEmail, stage.currentEmail) {
		return cancelAccountEmailChangeStage(ctx, store, active, identity, stage.pendingVerified)
	}
	if identity.HasVerifiedEmailAddress(stage.candidatePendingEmail) {
		return ErrAccountEmailChangeInFlight
	}
	return s.replaceAccountEmailChangeStage(ctx, store, active, identity, stage, memberID)
}

func cancelAccountEmailChangeStage(
	ctx context.Context,
	store Store,
	active *EmailChangeRequest,
	identity *auth.Identity,
	pendingVerified bool,
) error {
	if active == nil {
		return nil
	}
	if pendingVerified || identity.HasVerifiedEmailAddress(active.RequestedEmailAddress) {
		return ErrAccountEmailChangeInFlight
	}
	return store.DeleteEmailChangeRequest(ctx, active.ID)
}

func (s *AccountEmailChangeLifecycle) replaceAccountEmailChangeStage(
	ctx context.Context,
	store Store,
	active *EmailChangeRequest,
	identity *auth.Identity,
	stage accountEmailChangeStage,
	memberID string,
) error {
	if active != nil {
		if accountEmailsEqual(active.PreviousEmailAddress, stage.currentEmail) &&
			accountEmailsEqual(active.RequestedEmailAddress, stage.candidatePendingEmail) {
			return nil
		}
		if stage.pendingVerified || identity.HasVerifiedEmailAddress(active.RequestedEmailAddress) {
			return ErrAccountEmailChangeInFlight
		}
		if err := store.DeleteEmailChangeRequest(ctx, active.ID); err != nil {
			return err
		}
	}

	request := EmailChangeRequest{
		ID:                    uuid.NewString(),
		MemberID:              memberID,
		IdentityID:            stage.identityID,
		PreviousEmailAddress:  stage.currentEmail,
		RequestedEmailAddress: stage.candidatePendingEmail,
		CreatedAt:             s.now().UTC(),
	}
	if err := store.InsertEmailChangeRequest(ctx, request); err != nil {
		if errors.Is(err, ErrEmailChangeRequestExists) {
			return fmt.Errorf("%w: %w", ErrAccountEmailChangeConflict, err)
		}
		return err
	}
	return nil
}
