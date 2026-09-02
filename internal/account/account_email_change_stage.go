package account

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
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

	return identitystate.WithMutation(ctx, s.db, stage.identityID, func(
		mutationCtx context.Context,
		connection *gorm.DB,
	) error {
		identity, memberID, err := s.loadAccountEmailChangeStageState(mutationCtx, connection, stage)
		if err != nil {
			return err
		}
		return connection.Transaction(func(tx *gorm.DB) error {
			return s.persistAccountEmailChangeStage(tx, stage, identity, memberID)
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
	db *gorm.DB,
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
	memberID, err := authorizationtarget.ActiveMemberIDForIdentity(ctx, db, stage.identityID)
	if err != nil {
		return nil, "", fmt.Errorf("resolve account email change member: %w", err)
	}
	return identity, memberID, nil
}

func (s *AccountEmailChangeLifecycle) persistAccountEmailChangeStage(
	tx *gorm.DB,
	stage accountEmailChangeStage,
	identity *auth.Identity,
	memberID string,
) error {
	active, err := lockAccountEmailChangeRequest(tx, stage.identityID)
	if err != nil {
		return err
	}
	if stage.currentPendingEmail != "" {
		stage.pendingVerified = identity.HasVerifiedEmailAddress(stage.currentPendingEmail)
	}
	if stage.candidatePendingEmail == "" || accountEmailsEqual(stage.candidatePendingEmail, stage.currentEmail) {
		return cancelAccountEmailChangeStage(tx, active, identity, stage.pendingVerified)
	}
	if identity.HasVerifiedEmailAddress(stage.candidatePendingEmail) {
		return ErrAccountEmailChangeInFlight
	}
	return s.replaceAccountEmailChangeStage(tx, active, identity, stage, memberID)
}

func lockAccountEmailChangeRequest(tx *gorm.DB, identityID string) (*model.AccountEmailChangeRequest, error) {
	var active model.AccountEmailChangeRequest
	err := tx.Raw(`
		SELECT *
		FROM account_email_change_request
		WHERE identity_id = ?::uuid
		FOR UPDATE
	`, identityID).Scan(&active).Error
	return &active, err
}

func cancelAccountEmailChangeStage(
	tx *gorm.DB,
	active *model.AccountEmailChangeRequest,
	identity *auth.Identity,
	pendingVerified bool,
) error {
	if active.ID == "" {
		return nil
	}
	if pendingVerified || identity.HasVerifiedEmailAddress(active.RequestedEmailAddress) {
		return ErrAccountEmailChangeInFlight
	}
	return deleteAccountEmailChangeRequest(tx, active.ID)
}

func (s *AccountEmailChangeLifecycle) replaceAccountEmailChangeStage(
	tx *gorm.DB,
	active *model.AccountEmailChangeRequest,
	identity *auth.Identity,
	stage accountEmailChangeStage,
	memberID string,
) error {
	if active.ID != "" {
		if accountEmailsEqual(active.PreviousEmailAddress, stage.currentEmail) &&
			accountEmailsEqual(active.RequestedEmailAddress, stage.candidatePendingEmail) {
			return nil
		}
		if stage.pendingVerified || identity.HasVerifiedEmailAddress(active.RequestedEmailAddress) {
			return ErrAccountEmailChangeInFlight
		}
		if err := deleteAccountEmailChangeRequest(tx, active.ID); err != nil {
			return err
		}
	}

	request := model.AccountEmailChangeRequest{
		ID:                    uuid.NewString(),
		MemberID:              memberID,
		IdentityID:            stage.identityID,
		PreviousEmailAddress:  stage.currentEmail,
		RequestedEmailAddress: stage.candidatePendingEmail,
		CreatedAt:             s.now().UTC(),
	}
	if err := tx.Create(&request).Error; err != nil {
		if dberrors.IsUniqueViolation(err) {
			return ErrAccountEmailChangeConflict
		}
		return err
	}
	return nil
}

func deleteAccountEmailChangeRequest(db *gorm.DB, requestID string) error {
	result := db.Delete(&model.AccountEmailChangeRequest{}, "id = ?", requestID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
