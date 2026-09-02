// Package crypto provides cryptographic utilities for the backend.
package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// GenerateSecureToken generates a cryptographically secure random token.
// The token is 32 bytes (256 bits) of random data, base64url encoded.
// This produces a 43-character URL-safe string.
func GenerateSecureToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

// TokenPurpose identifies the purpose of a signed token.
type TokenPurpose string

const (
	// PurposeUnsubscribe is for unsubscribe tokens.
	PurposeUnsubscribe TokenPurpose = "u"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrTokenExpired = errors.New("token expired")
)

// SignedToken represents a validated signed token.
type SignedToken struct {
	ID      string
	Purpose TokenPurpose
	Expiry  time.Time
}

// GenerateSignedToken creates an HMAC-signed token.
// Format: base64url(id.purpose.expiry.signature)
// If expiry is zero, the token never expires.
func GenerateSignedToken(id string, purpose TokenPurpose, expiry time.Time, secret string) string {
	expiryUnix := int64(0)
	if !expiry.IsZero() {
		expiryUnix = expiry.Unix()
	}

	payload := fmt.Sprintf("%s.%s.%d", id, purpose, expiryUnix)
	signature := computeHMAC(payload, secret)

	token := fmt.Sprintf("%s.%s", payload, signature)
	return base64.RawURLEncoding.EncodeToString([]byte(token))
}

// ValidateSignedToken validates and parses a signed token.
// Returns the parsed token or an error if invalid/expired.
func ValidateSignedToken(token string, secret string) (*SignedToken, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, ErrInvalidToken
	}

	parts := strings.Split(string(decoded), ".")
	if len(parts) != 4 {
		return nil, ErrInvalidToken
	}

	id := parts[0]
	purpose := TokenPurpose(parts[1])
	expiryStr := parts[2]
	providedSig := parts[3]

	// Validate purpose
	if purpose != PurposeUnsubscribe {
		return nil, ErrInvalidToken
	}

	// Verify signature
	payload := fmt.Sprintf("%s.%s.%s", id, purpose, expiryStr)
	expectedSig := computeHMAC(payload, secret)
	if !hmac.Equal([]byte(providedSig), []byte(expectedSig)) {
		return nil, ErrInvalidToken
	}

	// Parse and check expiry
	expiryUnix, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil {
		return nil, ErrInvalidToken
	}

	var expiry time.Time
	if expiryUnix > 0 {
		expiry = time.Unix(expiryUnix, 0)
		if time.Now().After(expiry) {
			return nil, ErrTokenExpired
		}
	}

	return &SignedToken{
		ID:      id,
		Purpose: purpose,
		Expiry:  expiry,
	}, nil
}

// computeHMAC generates HMAC-SHA256 signature.
func computeHMAC(data, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// HashToken hashes a plaintext token using SHA256 for secure storage.
// This is a one-way hash used for storing tokens that need to be verified
// but should not be readable from the database.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", h[:])
}
