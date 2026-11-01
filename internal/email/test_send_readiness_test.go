package email

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// readinessRows answers the two counts the readiness check reads; any other
// Store call panics on the nil Store.
type readinessRows struct {
	Store
	activeAdapters int64
	suppressed     map[string]bool // lower-cased address: released
}

func (r *readinessRows) ActiveMailAdapterCount(context.Context) (int64, error) {
	return r.activeAdapters, nil
}

func (r *readinessRows) ActiveSuppressionCount(_ context.Context, address string) (int64, error) {
	if released, ok := r.suppressed[strings.ToLower(address)]; ok && !released {
		return 1, nil
	}
	return 0, nil
}

func TestCheckTestSendReadinessReportsDroppedTestEmails(t *testing.T) {
	t.Parallel()
	store := &readinessRows{suppressed: map[string]bool{}}
	ctx := t.Context()

	require.ErrorIs(t, CheckTestSendReadiness(ctx, store, "admin@example.test"), ErrNoActiveMailAdapter)
	store.activeAdapters = 1
	require.NoError(t, CheckTestSendReadiness(ctx, store, "admin@example.test"))

	store.suppressed["bounced@example.test"] = false
	err := CheckTestSendReadiness(ctx, store, " Bounced@Example.test ")
	require.ErrorIs(t, err, ErrTestRecipientSuppressed)
	require.Contains(t, TestSendReadinessMessage(err), "suppression list")

	store.suppressed["bounced@example.test"] = true
	require.NoError(t, CheckTestSendReadiness(ctx, store, "bounced@example.test"), "a released suppression no longer blocks")
}
