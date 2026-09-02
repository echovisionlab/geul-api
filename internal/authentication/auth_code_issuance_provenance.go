package authentication

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/email"
)

const (
	// AuthCodeIssuanceProvenanceNamespace is the Kratos transient-payload key
	// consumed by the trusted courier.
	AuthCodeIssuanceProvenanceNamespace = "__geul_auth_issuance"
	authCodeIssuanceProvenanceVersion   = "v1"
)

// AuthCodeIssuanceProvenance binds a courier code to its accepted public
// issuance without exposing the reservation table.
type AuthCodeIssuanceProvenance struct {
	Version    string `json:"version"`
	IssuanceID string `json:"issuance_id"`
	IssuedAt   string `json:"issued_at"`
	Purpose    string `json:"purpose"`
	Recipient  string `json:"recipient"`
	MAC        string `json:"mac"`
}

// NewAuthCodeIssuanceProvenance creates a signed courier provenance payload.
func NewAuthCodeIssuanceProvenance(
	secret []byte,
	eventKey email.EventKey,
	recipient string,
	issuanceID string,
	issuedAt time.Time,
) (AuthCodeIssuanceProvenance, error) {
	if len(secret) == 0 {
		return AuthCodeIssuanceProvenance{}, errors.New("auth issuance provenance key is required")
	}
	if !isAuthCodeEvent(eventKey) {
		return AuthCodeIssuanceProvenance{}, fmt.Errorf("unsupported auth issuance purpose: %q", eventKey)
	}
	normalizedRecipient := email.NormalizeAddressForDelivery(recipient)
	if normalizedRecipient == "" {
		return AuthCodeIssuanceProvenance{}, errors.New("auth issuance recipient is required")
	}
	issuanceID = strings.TrimSpace(issuanceID)
	if !validAuthCodeIssuanceID(issuanceID) {
		return AuthCodeIssuanceProvenance{}, errors.New("auth issuance id must be 256-bit lowercase hexadecimal")
	}
	if issuedAt.IsZero() {
		return AuthCodeIssuanceProvenance{}, errors.New("auth issuance time is required")
	}
	issuedAtText := issuedAt.UTC().Format(time.RFC3339Nano)
	provenance := AuthCodeIssuanceProvenance{
		Version:    authCodeIssuanceProvenanceVersion,
		IssuanceID: issuanceID,
		IssuedAt:   issuedAtText,
		Purpose:    eventKey.String(),
		Recipient:  normalizedRecipient,
	}
	provenance.MAC = authCodeIssuanceProvenanceMAC(secret, provenance)
	return provenance, nil
}

// VerifyAuthCodeIssuanceProvenance validates the signed courier payload.
func VerifyAuthCodeIssuanceProvenance(
	secret []byte,
	eventKey email.EventKey,
	recipient string,
	provenance AuthCodeIssuanceProvenance,
) (time.Time, error) {
	if len(secret) == 0 {
		return time.Time{}, errors.New("auth issuance provenance key is required")
	}
	if provenance.Version != authCodeIssuanceProvenanceVersion {
		return time.Time{}, errors.New("auth issuance provenance version is invalid")
	}
	if !validAuthCodeIssuanceID(provenance.IssuanceID) {
		return time.Time{}, errors.New("auth issuance provenance id is invalid")
	}
	issuedAt, err := time.Parse(time.RFC3339Nano, provenance.IssuedAt)
	if err != nil || provenance.IssuedAt != issuedAt.UTC().Format(time.RFC3339Nano) {
		return time.Time{}, errors.New("auth issuance provenance time is invalid")
	}
	if provenance.Purpose != eventKey.String() {
		return time.Time{}, errors.New("auth issuance provenance purpose is invalid")
	}
	normalizedRecipient := email.NormalizeAddressForDelivery(recipient)
	if normalizedRecipient == "" || provenance.Recipient != normalizedRecipient {
		return time.Time{}, errors.New("auth issuance provenance recipient is invalid")
	}
	providedMAC, err := hex.DecodeString(provenance.MAC)
	if err != nil || len(providedMAC) != sha256.Size || provenance.MAC != strings.ToLower(provenance.MAC) {
		return time.Time{}, errors.New("auth issuance provenance signature is invalid")
	}
	expectedMAC, err := hex.DecodeString(authCodeIssuanceProvenanceMAC(secret, provenance))
	if err != nil || !hmac.Equal(providedMAC, expectedMAC) {
		return time.Time{}, errors.New("auth issuance provenance signature is invalid")
	}
	return issuedAt.UTC(), nil
}

func authCodeIssuanceProvenanceMAC(
	secret []byte,
	provenance AuthCodeIssuanceProvenance,
) string {
	keyDerivation := hmac.New(sha256.New, secret)
	_, _ = keyDerivation.Write([]byte("geul/auth-issuance-provenance/v1"))
	mac := hmac.New(sha256.New, keyDerivation.Sum(nil))
	for _, value := range []string{
		provenance.Version,
		provenance.IssuanceID,
		provenance.IssuedAt,
		provenance.Purpose,
		provenance.Recipient,
	} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = mac.Write(length[:])
		_, _ = mac.Write([]byte(value))
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func validAuthCodeIssuanceID(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

// AuthCodeIdempotencyKey derives the stable queue key for one code issuance.
func AuthCodeIdempotencyKey(
	eventKey email.EventKey,
	recipient string,
	issuanceID string,
) (string, error) {
	normalizedRecipient := email.NormalizeAddressForDelivery(recipient)
	if !isAuthCodeEvent(eventKey) || normalizedRecipient == "" || !validAuthCodeIssuanceID(issuanceID) {
		return "", errors.New("authentication email idempotency ownership is invalid")
	}
	digest := sha256.New()
	for _, value := range []string{eventKey.String(), normalizedRecipient, issuanceID} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(value))
	}
	return "auth-code:v1:" + hex.EncodeToString(digest.Sum(nil)), nil
}
