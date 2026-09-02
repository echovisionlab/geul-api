// Package crypto provides cryptographic utilities for the backend.
package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2idParams holds the parameters for argon2id hashing.
// OWASP recommended minimum values (2023):
// - Memory: 19456 KiB (19 MiB)
// - Iterations: 2
// - Parallelism: 1
// - Salt length: 16 bytes
// - Key length: 32 bytes
type Argon2idParams struct {
	Memory      uint32 // Memory usage in KiB
	Iterations  uint32 // Number of iterations (time cost)
	Parallelism uint8  // Number of parallel threads
	SaltLength  uint32 // Length of the salt in bytes
	KeyLength   uint32 // Length of the derived key in bytes
}

// DefaultParams returns OWASP-recommended argon2id parameters.
func DefaultParams() *Argon2idParams {
	return &Argon2idParams{
		Memory:      19456, // 19 MiB
		Iterations:  2,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
	}
}

var (
	ErrInvalidHash         = errors.New("invalid argon2id hash format")
	ErrIncompatibleVersion = errors.New("incompatible argon2id version")
)

// PasswordHasher provides password hashing and verification using argon2id.
type PasswordHasher struct {
	params *Argon2idParams
}

// NewPasswordHasher creates a new PasswordHasher with the given parameters.
// If params is nil, DefaultParams() is used.
func NewPasswordHasher(params *Argon2idParams) *PasswordHasher {
	if params == nil {
		params = DefaultParams()
	}
	return &PasswordHasher{params: params}
}

// Hash generates an argon2id hash of the password.
// Returns the hash in PHC string format:
// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
func (h *PasswordHasher) Hash(password string) (string, error) {
	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}

	hash := argon2.IDKey(
		[]byte(password),
		salt,
		h.params.Iterations,
		h.params.Memory,
		h.params.Parallelism,
		h.params.KeyLength,
	)

	// Encode to PHC string format
	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		h.params.Memory,
		h.params.Iterations,
		h.params.Parallelism,
		b64Salt,
		b64Hash,
	), nil
}

// Verify compares a password against an argon2id hash.
// Returns true if the password matches.
func (h *PasswordHasher) Verify(password, encodedHash string) (bool, error) {
	params, salt, hash, err := decodeHash(encodedHash)
	if err != nil {
		return false, err
	}

	// Derive the key from the password with the extracted parameters
	otherHash := argon2.IDKey(
		[]byte(password),
		salt,
		params.Iterations,
		params.Memory,
		params.Parallelism,
		params.KeyLength,
	)

	// Constant-time comparison to prevent timing attacks
	return subtle.ConstantTimeCompare(hash, otherHash) == 1, nil
}

// NeedsRehash checks if the hash should be rehashed with current parameters.
// This is useful for upgrading hash strength over time.
func (h *PasswordHasher) NeedsRehash(encodedHash string) bool {
	params, _, _, err := decodeHash(encodedHash)
	if err != nil {
		return true
	}

	return params.Memory != h.params.Memory ||
		params.Iterations != h.params.Iterations ||
		params.Parallelism != h.params.Parallelism ||
		params.KeyLength != h.params.KeyLength
}

// decodeHash extracts the parameters, salt, and hash from a PHC-formatted string.
func decodeHash(encodedHash string) (*Argon2idParams, []byte, []byte, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 {
		return nil, nil, nil, ErrInvalidHash
	}

	if parts[1] != "argon2id" {
		return nil, nil, nil, ErrInvalidHash
	}

	var version int
	_, err := fmt.Sscanf(parts[2], "v=%d", &version)
	if err != nil {
		return nil, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return nil, nil, nil, ErrIncompatibleVersion
	}

	var memory, iterations uint32
	var parallelism uint8
	_, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism)
	if err != nil {
		return nil, nil, nil, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, nil, nil, ErrInvalidHash
	}

	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return nil, nil, nil, ErrInvalidHash
	}

	params := &Argon2idParams{
		Memory:      memory,
		Iterations:  iterations,
		Parallelism: parallelism,
		SaltLength:  uint32(len(salt)),
		KeyLength:   uint32(len(hash)),
	}

	return params, salt, hash, nil
}

// IsBcryptHash checks if the hash is a bcrypt hash (starts with $2a$, $2b$, or $2y$).
// This is used for backward compatibility during migration.
func IsBcryptHash(hash string) bool {
	return strings.HasPrefix(hash, "$2a$") ||
		strings.HasPrefix(hash, "$2b$") ||
		strings.HasPrefix(hash, "$2y$")
}
