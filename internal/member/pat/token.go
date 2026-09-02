package pat

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	tokenPrefix        = "geul_pat_"
	tokenSelectorBytes = 16
	tokenSecretBytes   = 32
)

var ErrInvalidToken = errors.New("personal access token is invalid")

// Secret contains a newly issued bearer value. It deliberately redacts all
// fmt output; Reveal must be called explicitly at the one-time delivery edge.
type Secret struct {
	raw string
}

// Reveal returns the bearer value for its one-time delivery to the Member.
func (secret Secret) Reveal() string { return secret.raw }

func (Secret) String() string   { return "[REDACTED]" }
func (Secret) GoString() string { return "[REDACTED]" }

// Verifier is the one-way value persisted for later bearer verification.
type Verifier struct {
	digest [sha256.Size]byte
}

// VerifierFromBytes reconstructs a persisted verifier without accepting a
// plaintext secret.
func VerifierFromBytes(value []byte) (Verifier, error) {
	if len(value) != sha256.Size {
		return Verifier{}, ErrInvalidToken
	}
	var verifier Verifier
	copy(verifier.digest[:], value)
	return verifier, nil
}

// Bytes returns a defensive copy suitable for persistence.
func (verifier Verifier) Bytes() []byte {
	value := make([]byte, len(verifier.digest))
	copy(value, verifier.digest[:])
	return value
}

func (Verifier) String() string   { return "[REDACTED]" }
func (Verifier) GoString() string { return "[REDACTED]" }

func (verifier Verifier) valid() bool {
	var zero [sha256.Size]byte
	return subtle.ConstantTimeCompare(verifier.digest[:], zero[:]) != 1
}

func (verifier Verifier) matches(candidate Verifier) bool {
	return subtle.ConstantTimeCompare(verifier.digest[:], candidate.digest[:]) == 1
}

type generatedCredential struct {
	id       TokenID
	secret   Secret
	verifier Verifier
}

func generateCredential(random io.Reader) (generatedCredential, error) {
	selector := make([]byte, tokenSelectorBytes)
	if _, err := io.ReadFull(random, selector); err != nil {
		return generatedCredential{}, fmt.Errorf("generate personal access token selector: %w", err)
	}
	selectorText := base64.RawURLEncoding.EncodeToString(selector)
	clear(selector)
	return generateCredentialForID(TokenID(selectorText), random)
}

func generateCredentialForID(tokenID TokenID, random io.Reader) (generatedCredential, error) {
	if !validTokenID(tokenID) {
		return generatedCredential{}, ErrInvalidToken
	}
	secret := make([]byte, tokenSecretBytes)
	if _, err := io.ReadFull(random, secret); err != nil {
		return generatedCredential{}, fmt.Errorf("generate personal access token secret: %w", err)
	}
	secretText := base64.RawURLEncoding.EncodeToString(secret)
	verifier := verifierForSecret(secret)
	clear(secret)
	return generatedCredential{
		id:       tokenID,
		secret:   Secret{raw: tokenPrefix + string(tokenID) + "." + secretText},
		verifier: verifier,
	}, nil
}

func parseToken(raw string) (TokenID, Verifier, error) {
	remainder, ok := strings.CutPrefix(raw, tokenPrefix)
	if !ok {
		return "", Verifier{}, ErrInvalidToken
	}
	selectorText, secretText, ok := strings.Cut(remainder, ".")
	if !ok || strings.Contains(secretText, ".") {
		return "", Verifier{}, ErrInvalidToken
	}

	selector, err := base64.RawURLEncoding.DecodeString(selectorText)
	if err != nil || len(selector) != tokenSelectorBytes || base64.RawURLEncoding.EncodeToString(selector) != selectorText {
		clear(selector)
		return "", Verifier{}, ErrInvalidToken
	}
	secret, err := base64.RawURLEncoding.DecodeString(secretText)
	if err != nil || len(secret) != tokenSecretBytes || base64.RawURLEncoding.EncodeToString(secret) != secretText {
		clear(selector)
		clear(secret)
		return "", Verifier{}, ErrInvalidToken
	}

	verifier := verifierForSecret(secret)
	clear(selector)
	clear(secret)
	return TokenID(selectorText), verifier, nil
}

func verifierForSecret(secret []byte) Verifier {
	return Verifier{digest: sha256.Sum256(secret)}
}

func validTokenID(tokenID TokenID) bool {
	encoded := string(tokenID)
	selector, err := base64.RawURLEncoding.DecodeString(encoded)
	valid := err == nil && len(selector) == tokenSelectorBytes &&
		base64.RawURLEncoding.EncodeToString(selector) == encoded
	clear(selector)
	return valid
}
