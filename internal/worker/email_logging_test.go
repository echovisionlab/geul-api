package worker

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/emaildelivery"
)

func TestDurableEmailMessageIDHasNoRandomFallback(t *testing.T) {
	require.Empty(t, emaildelivery.DurableMessageID("", "", ""))
	require.Equal(t, "<command-123@localhost>", emaildelivery.DurableMessageID("", "command-123"))
}

func TestRetryableEmailDeliveryErrorDoesNotExposeProviderResponse(t *testing.T) {
	err := retryableEmailDeliveryError("rate_limited")
	require.EqualError(t, err, "mail delivery retry requested: rate_limited")
	require.NotContains(t, err.Error(), "@")
}
