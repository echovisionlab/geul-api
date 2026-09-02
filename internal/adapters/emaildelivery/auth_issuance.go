package emaildeliveryadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
)

type AuthIssuanceAuthority struct {
	key          []byte
	reservations *authentication.AuthCodeIssuanceLimiter
	authorizer   *account.AccountEmailChangeLifecycle
}

func NewAuthIssuanceAuthority(
	key []byte,
	reservations *authentication.AuthCodeIssuanceLimiter,
	authorizer *account.AccountEmailChangeLifecycle,
) *AuthIssuanceAuthority {
	if len(key) == 0 {
		panic("email courier issuance key is required")
	}
	if (reservations == nil) != (authorizer == nil) {
		panic("settings verification reservation and authorizer must be configured together")
	}
	return &AuthIssuanceAuthority{
		key:          append([]byte(nil), key...),
		reservations: reservations,
		authorizer:   authorizer,
	}
}

func (a *AuthIssuanceAuthority) Verify(
	eventKey email.EventKey,
	recipient string,
	provenance emaildelivery.AuthIssuanceProvenance,
) (emaildelivery.AuthIssuance, error) {
	issuedAt, err := authentication.VerifyAuthCodeIssuanceProvenance(
		a.key,
		eventKey,
		recipient,
		authentication.AuthCodeIssuanceProvenance{
			Version: provenance.Version, IssuanceID: provenance.IssuanceID,
			IssuedAt: provenance.IssuedAt, Purpose: provenance.Purpose,
			Recipient: provenance.Recipient, MAC: provenance.MAC,
		},
	)
	if err != nil {
		return emaildelivery.AuthIssuance{}, err
	}
	return emaildelivery.AuthIssuance{IssuanceID: provenance.IssuanceID, IssuedAt: issuedAt}, nil
}

func (a *AuthIssuanceAuthority) RestoreSettingsVerification(
	ctx context.Context,
	eventKey email.EventKey,
	recipient string,
	identityID string,
) (emaildelivery.AuthIssuance, bool, error) {
	if a.reservations == nil || a.authorizer == nil {
		return emaildelivery.AuthIssuance{}, false, nil
	}
	authorized, err := a.authorizer.AuthorizeSettingsGeneratedVerification(ctx, identityID, recipient)
	if err != nil || !authorized {
		return emaildelivery.AuthIssuance{}, false, err
	}
	reservation, found, err := a.reservations.CurrentReservation(ctx, eventKey, recipient)
	if err != nil || !found {
		return emaildelivery.AuthIssuance{}, false, err
	}
	return emaildelivery.AuthIssuance{
		IssuanceID: reservation.IssuanceID(),
		IssuedAt:   reservation.IssuedAt(),
	}, true, nil
}

func (*AuthIssuanceAuthority) IdempotencyKey(
	eventKey email.EventKey,
	recipient string,
	issuanceID string,
) (string, error) {
	return authentication.AuthCodeIdempotencyKey(eventKey, recipient, issuanceID)
}
