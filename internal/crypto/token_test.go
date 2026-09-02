package crypto

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateSecureToken(t *testing.T) {
	token, err := GenerateSecureToken()
	require.NoError(t, err)
	assert.Len(t, token, 43)

	decoded, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)
	assert.Len(t, decoded, 32)
}

func TestSignedTokenRoundTrip(t *testing.T) {
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	token := GenerateSignedToken("newsletter:identity-1", PurposeUnsubscribe, expiry, "secret")

	parsed, err := ValidateSignedToken(token, "secret")
	require.NoError(t, err)
	assert.Equal(t, "newsletter:identity-1", parsed.ID)
	assert.Equal(t, PurposeUnsubscribe, parsed.Purpose)
	assert.Equal(t, expiry.Unix(), parsed.Expiry.Unix())
}

func TestSignedTokenWithoutExpiry(t *testing.T) {
	token := GenerateSignedToken("newsletter:identity-1", PurposeUnsubscribe, time.Time{}, "secret")

	parsed, err := ValidateSignedToken(token, "secret")
	require.NoError(t, err)
	assert.Equal(t, "newsletter:identity-1", parsed.ID)
	assert.Equal(t, PurposeUnsubscribe, parsed.Purpose)
	assert.True(t, parsed.Expiry.IsZero())
}

func TestSignedTokenRejectsInvalidInput(t *testing.T) {
	validToken := GenerateSignedToken("newsletter:identity-1", PurposeUnsubscribe, time.Now().Add(time.Hour), "secret")
	decoded, err := base64.RawURLEncoding.DecodeString(validToken)
	require.NoError(t, err)
	parts := strings.Split(string(decoded), ".")
	require.Len(t, parts, 4)

	for _, tc := range []struct {
		name  string
		token string
		err   error
	}{
		{name: "not base64", token: "not base64", err: ErrInvalidToken},
		{name: "wrong shape", token: base64.RawURLEncoding.EncodeToString([]byte("a.b.c")), err: ErrInvalidToken},
		{name: "unsupported purpose", token: GenerateSignedToken("newsletter:identity-1", TokenPurpose("x"), time.Now().Add(time.Hour), "secret"), err: ErrInvalidToken},
		{name: "wrong secret", token: validToken, err: ErrInvalidToken},
		{name: "bad expiry", token: encodeTokenParts(parts[0], parts[1], "not-unix", parts[3]), err: ErrInvalidToken},
		{name: "tampered signature", token: encodeTokenParts(parts[0], parts[1], parts[2], "tampered"), err: ErrInvalidToken},
		{name: "expired", token: GenerateSignedToken("newsletter:identity-1", PurposeUnsubscribe, time.Now().Add(-time.Hour), "secret"), err: ErrTokenExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secret := "secret"
			if tc.name == "wrong secret" {
				secret = "other-secret"
			}

			parsed, err := ValidateSignedToken(tc.token, secret)
			assert.Nil(t, parsed)
			assert.True(t, errors.Is(err, tc.err))
		})
	}
}

func TestHashToken(t *testing.T) {
	first := HashToken("token")
	second := HashToken("token")
	other := HashToken("other-token")

	assert.Len(t, first, 64)
	assert.Equal(t, first, second)
	assert.NotEqual(t, first, other)
}

func encodeTokenParts(id, purpose, expiry, signature string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join([]string{id, purpose, expiry, signature}, ".")))
}
