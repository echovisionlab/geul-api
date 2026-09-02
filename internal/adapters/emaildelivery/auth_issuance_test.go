package emaildeliveryadapter

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/stretchr/testify/require"
)

func TestAuthIssuanceAuthorityVerifiesProviderNeutralProof(t *testing.T) {
	key := []byte("email-delivery-auth-issuance-test-secret")
	issuedAt := time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)
	issuanceDigest := sha256.Sum256([]byte("issuance-1"))
	issuanceID := hex.EncodeToString(issuanceDigest[:])
	proof, err := authentication.NewAuthCodeIssuanceProvenance(
		key, email.EventLoginCode, "reader@example.test", issuanceID, issuedAt,
	)
	require.NoError(t, err)
	authority := NewAuthIssuanceAuthority(key, nil, nil)

	issuance, err := authority.Verify(
		email.EventLoginCode,
		"reader@example.test",
		emaildelivery.AuthIssuanceProvenance{
			Version: proof.Version, IssuanceID: proof.IssuanceID,
			IssuedAt: proof.IssuedAt, Purpose: proof.Purpose,
			Recipient: proof.Recipient, MAC: proof.MAC,
		},
	)
	require.NoError(t, err)
	require.Equal(t, issuanceID, issuance.IssuanceID)
	require.Equal(t, issuedAt, issuance.IssuedAt)

	_, found, err := authority.RestoreSettingsVerification(
		t.Context(), email.EventVerificationCode, "reader@example.test", "identity-1",
	)
	require.NoError(t, err)
	require.False(t, found)
	messageID, err := authority.IdempotencyKey(email.EventLoginCode, "reader@example.test", issuance.IssuanceID)
	require.NoError(t, err)
	require.NotEmpty(t, messageID)
}

func TestNewAuthIssuanceAuthorityRequiresCompleteConfiguration(t *testing.T) {
	require.Panics(t, func() { NewAuthIssuanceAuthority(nil, nil, nil) })
	require.Panics(t, func() {
		NewAuthIssuanceAuthority([]byte("secret"), &authentication.AuthCodeIssuanceLimiter{}, nil)
	})
}
