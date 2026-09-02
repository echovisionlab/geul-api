package crypto

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPasswordHasherHashVerifyAndRehash(t *testing.T) {
	hasher := NewPasswordHasher(testArgon2Params())

	hash, err := hasher.Hash("correct horse battery staple")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(hash, "$argon2id$"))
	assert.False(t, IsBcryptHash(hash))
	assert.False(t, hasher.NeedsRehash(hash))

	ok, err := hasher.Verify("correct horse battery staple", hash)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = hasher.Verify("wrong password", hash)
	require.NoError(t, err)
	assert.False(t, ok)

	strongerHasher := NewPasswordHasher(&Argon2idParams{
		Memory:      128,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  8,
		KeyLength:   16,
	})
	assert.True(t, strongerHasher.NeedsRehash(hash))
}

func TestPasswordHasherUsesUniqueSalt(t *testing.T) {
	hasher := NewPasswordHasher(testArgon2Params())

	first, err := hasher.Hash("same password")
	require.NoError(t, err)
	second, err := hasher.Hash("same password")
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
}

func TestPasswordHasherRejectsInvalidHashFormats(t *testing.T) {
	hasher := NewPasswordHasher(testArgon2Params())

	for _, tc := range []struct {
		name string
		hash string
		err  error
	}{
		{name: "empty", hash: "", err: ErrInvalidHash},
		{name: "wrong algorithm", hash: "$bcrypt$v=19$m=64,t=1,p=1$salt$hash", err: ErrInvalidHash},
		{name: "bad version", hash: "$argon2id$v=not-a-number$m=64,t=1,p=1$c2FsdA$aGFzaA", err: ErrInvalidHash},
		{name: "incompatible version", hash: "$argon2id$v=18$m=64,t=1,p=1$c2FsdA$aGFzaA", err: ErrIncompatibleVersion},
		{name: "bad params", hash: "$argon2id$v=19$m=bad,t=1,p=1$c2FsdA$aGFzaA", err: ErrInvalidHash},
		{name: "bad salt", hash: "$argon2id$v=19$m=64,t=1,p=1$%%%$aGFzaA", err: ErrInvalidHash},
		{name: "bad hash", hash: "$argon2id$v=19$m=64,t=1,p=1$c2FsdA$%%%", err: ErrInvalidHash},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := hasher.Verify("password", tc.hash)
			assert.False(t, ok)
			assert.True(t, errors.Is(err, tc.err))
			assert.True(t, hasher.NeedsRehash(tc.hash))
		})
	}
}

func TestPasswordHashTypeDetection(t *testing.T) {
	for _, hash := range []string{
		"$2a$10$abcdefghijklmnopqrstuu",
		"$2b$10$abcdefghijklmnopqrstuu",
		"$2y$10$abcdefghijklmnopqrstuu",
	} {
		assert.True(t, IsBcryptHash(hash))
	}

	assert.False(t, IsBcryptHash("$argon2id$v=19$m=64,t=1,p=1$salt$hash"))
	assert.False(t, IsBcryptHash(strings.TrimPrefix("$2b$10$abcdefghijklmnopqrstuu", "$")))
}

func testArgon2Params() *Argon2idParams {
	return &Argon2idParams{
		Memory:      64,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  8,
		KeyLength:   16,
	}
}
