package account

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
)

var (
	ErrAccountSettingsHookInput      = errors.New("account settings hook input is invalid")
	ErrCanonicalEmailGuardFailed     = errors.New("canonical account email guard failed")
	ErrCanonicalEmailChangeForbidden = errors.New("direct canonical account email change is forbidden")
)

type AccountSettingsIdentityReader interface {
	GetIdentity(context.Context, string) (*auth.Identity, error)
}

type AccountEmailChangeHookLifecycle interface {
	StageOrCancel(context.Context, string, string, string, string, string, bool) error
	VerifyAndReconcile(context.Context, string, string, string, string) error
}

type AccountSettingsHookInput struct {
	FlowID       string
	IdentityID   string
	Email        string
	PendingEmail string
}

// AccountSettingsHookService owns canonical-email policy and the durable
// account-email transition. HTTP handlers only decode and map typed errors.
type AccountSettingsHookService struct {
	identities   AccountSettingsIdentityReader
	emailChanges AccountEmailChangeHookLifecycle
}

func NewAccountSettingsHookService(
	identities AccountSettingsIdentityReader,
	emailChanges AccountEmailChangeHookLifecycle,
) *AccountSettingsHookService {
	if identities == nil || emailChanges == nil {
		panic("account settings identity and email-change ports are required")
	}
	return &AccountSettingsHookService{identities: identities, emailChanges: emailChanges}
}

func (service *AccountSettingsHookService) Stage(
	ctx context.Context,
	input AccountSettingsHookInput,
) error {
	if strings.TrimSpace(input.IdentityID) == "" || strings.TrimSpace(input.FlowID) == "" {
		return ErrAccountSettingsHookInput
	}
	committed, err := service.identities.GetIdentity(ctx, input.IdentityID)
	if err != nil {
		return errors.Join(ErrCanonicalEmailGuardFailed, err)
	}
	if committed == nil || strings.TrimSpace(committed.CurrentEmail()) == "" {
		return ErrCanonicalEmailGuardFailed
	}
	if !strings.EqualFold(strings.TrimSpace(committed.CurrentEmail()), strings.TrimSpace(input.Email)) {
		return ErrCanonicalEmailChangeForbidden
	}
	currentPendingEmail := ""
	if pending := committed.GetTraitString("pending_email"); pending != nil {
		currentPendingEmail = strings.TrimSpace(*pending)
	}
	return service.emailChanges.StageOrCancel(
		ctx,
		input.FlowID,
		input.IdentityID,
		committed.CurrentEmail(),
		currentPendingEmail,
		input.PendingEmail,
		currentPendingEmail != "" && committed.HasVerifiedEmailAddress(currentPendingEmail),
	)
}

func (service *AccountSettingsHookService) Verify(
	ctx context.Context,
	input AccountSettingsHookInput,
) error {
	if strings.TrimSpace(input.IdentityID) == "" || strings.TrimSpace(input.FlowID) == "" {
		return ErrAccountSettingsHookInput
	}
	if strings.TrimSpace(input.PendingEmail) == "" {
		return nil
	}
	return service.emailChanges.VerifyAndReconcile(
		ctx,
		input.FlowID,
		input.IdentityID,
		input.Email,
		input.PendingEmail,
	)
}
