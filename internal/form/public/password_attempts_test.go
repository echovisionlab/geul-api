package public

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/crypto"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/form"
)

func TestFormPasswordGuessesExhaustThePerClientBudgetUntilTheWindowPasses(t *testing.T) {
	t.Parallel()
	hasher := crypto.NewPasswordHasher(&crypto.Argon2idParams{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 16})
	hash, err := hasher.Hash("open-sesame")
	require.NoError(t, err)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	svc := &Service{password: hasher, attempts: newPasswordAttempts(func() time.Time { return now })}
	found := &form.Form{ID: "form-a", AccessPassword: &hash}

	matched, err := svc.checkFormPassword(t.Context(), found, "203.0.113.7", "open-sesame")
	require.NoError(t, err)
	assert.True(t, matched)
	for range formPasswordAttemptLimit {
		matched, err = svc.checkFormPassword(t.Context(), found, "203.0.113.7", "guess")
		require.NoError(t, err)
		assert.False(t, matched)
	}

	_, err = svc.checkFormPassword(t.Context(), found, "203.0.113.7", "open-sesame")
	connectErr, classified := errs.Classified(err)
	require.True(t, classified)
	assert.Equal(t, connect.CodeResourceExhausted, connectErr.Code(), "a correct password must not be confirmed once the budget is spent")

	matched, err = svc.checkFormPassword(t.Context(), found, "198.51.100.9", "open-sesame")
	require.NoError(t, err)
	assert.True(t, matched, "another client keeps its own budget")
	matched, err = svc.checkFormPassword(t.Context(), &form.Form{ID: "form-b", AccessPassword: &hash}, "203.0.113.7", "open-sesame")
	require.NoError(t, err)
	assert.True(t, matched, "another Form keeps its own budget")

	now = now.Add(formPasswordAttemptWindow)
	matched, err = svc.checkFormPassword(t.Context(), found, "203.0.113.7", "open-sesame")
	require.NoError(t, err)
	assert.True(t, matched)
}
