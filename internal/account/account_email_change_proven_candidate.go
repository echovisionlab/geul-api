package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
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
	return identitystate.WithMutation(ctx, s.db, identityID, func(
		mutationCtx context.Context,
		connection *gorm.DB,
	) error {
		return s.applyProvenCandidateMutation(mutationCtx, connection, identityID, targetEmail)
	})
}

type provenAccountEmailCandidate struct {
	identity      *auth.Identity
	memberID      string
	previousEmail string
	displayEmail  string
}

type provenAccountEmailStage struct {
	request  model.AccountEmailChangeRequest
	noChange bool
}

func (s *AccountEmailChangeLifecycle) applyProvenCandidateMutation(
	ctx context.Context,
	db *gorm.DB,
	identityID string,
	targetEmail string,
) error {
	candidate, err := s.loadProvenAccountEmailCandidate(ctx, db, identityID, targetEmail)
	if err != nil {
		return err
	}
	stage, err := s.stageProvenAccountEmailCandidate(db, identityID, targetEmail, candidate)
	if err != nil || stage.noChange {
		return err
	}
	if accountEmailsEqual(candidate.previousEmail, targetEmail) {
		return s.completeAccountEmailChangeRequest(ctx, db, &stage.request)
	}
	if err := s.applyProvenAccountEmailIdentity(ctx, db, identityID, candidate, &stage.request); err != nil {
		return err
	}
	return s.completeAccountEmailChangeRequest(ctx, db, &stage.request)
}

func (s *AccountEmailChangeLifecycle) loadProvenAccountEmailCandidate(
	ctx context.Context,
	db *gorm.DB,
	identityID string,
	targetEmail string,
) (provenAccountEmailCandidate, error) {
	identity, err := LoadIdentityWithEmailCredentials(ctx, s.identity, identityID)
	if err != nil {
		return provenAccountEmailCandidate{}, err
	}
	if identity == nil {
		return provenAccountEmailCandidate{}, gorm.ErrRecordNotFound
	}
	memberID, err := authorizationtarget.ActiveMemberIDForIdentity(ctx, db, identityID)
	if err != nil {
		return provenAccountEmailCandidate{}, fmt.Errorf("resolve account email change member: %w", err)
	}
	providerCandidates := ResolveAccountEmailProviderCandidates(ctx, identity.Credentials)
	rows, err := NewAccountEmailService(db, s.identity, s.memberEmails).
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
	db *gorm.DB,
	identityID string,
	targetEmail string,
	candidate provenAccountEmailCandidate,
) (provenAccountEmailStage, error) {
	stage := provenAccountEmailStage{}
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT *
			FROM account_email_change_request
			WHERE identity_id = ?::uuid
			FOR UPDATE
		`, identityID).Scan(&stage.request).Error; err != nil {
			return err
		}
		if accountEmailsEqual(candidate.previousEmail, targetEmail) {
			return reconcileProvenCandidateNoChange(tx, candidate.identity, targetEmail, &stage)
		}
		keep, err := reconcileProvenCandidateActiveRequest(
			tx, candidate.identity, candidate.previousEmail, targetEmail, &stage.request,
		)
		if err != nil || keep {
			return err
		}
		stage.request = model.AccountEmailChangeRequest{
			ID: uuid.NewString(), MemberID: candidate.memberID, IdentityID: identityID,
			PreviousEmailAddress: candidate.previousEmail, RequestedEmailAddress: targetEmail,
			CreatedAt: s.now().UTC(),
		}
		if err := tx.Create(&stage.request).Error; dberrors.IsUniqueViolation(err) {
			return ErrAccountEmailChangeConflict
		} else {
			return err
		}
	})
	return stage, err
}

func reconcileProvenCandidateNoChange(
	tx *gorm.DB,
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
	if err := deleteAccountEmailChangeRequest(tx, stage.request.ID); err != nil {
		return err
	}
	stage.request = model.AccountEmailChangeRequest{}
	stage.noChange = true
	return nil
}

func reconcileProvenCandidateActiveRequest(
	tx *gorm.DB,
	identity *auth.Identity,
	previousEmail string,
	targetEmail string,
	active *model.AccountEmailChangeRequest,
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
	if err := deleteAccountEmailChangeRequest(tx, active.ID); err != nil {
		return false, err
	}
	return false, nil
}

func (s *AccountEmailChangeLifecycle) applyProvenAccountEmailIdentity(
	ctx context.Context,
	db *gorm.DB,
	identityID string,
	candidate provenAccountEmailCandidate,
	request *model.AccountEmailChangeRequest,
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
	if finishErr := s.finishAccountEmailChangeRequest(db, request, "address_conflict"); finishErr != nil {
		return finishErr
	}
	return fmt.Errorf("%w: %v", ErrAccountEmailChangeConflict, err)
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
