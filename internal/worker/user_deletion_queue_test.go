package worker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func TestUserDeletionQueuesUseBoundedRetryAndDLQ(t *testing.T) {
	for _, queueName := range []string{
		eventpkg.QueueUserDeleteIdentity,
		eventpkg.QueueUserDeleteAvatar,
	} {
		config := userDeletionQueueConfig(queueName)
		require.Equal(t, queueName, config.Name)
		require.NotEmpty(t, config.MessageType)
		require.Equal(t, 5, config.MaxRetries)
		require.Equal(t, 5*time.Second, config.RetryDelay)
		require.Equal(t, float64(2), config.RetryBackoff)
	}
}

func TestUserDeletionCommandRequiresMemberUUID(t *testing.T) {
	require.Error(t, validateUserDeletionCommandMemberID(""))
	require.Error(t, validateUserDeletionCommandMemberID("not-a-uuid"))
	require.NoError(t, validateUserDeletionCommandMemberID("14600000-0000-1000-8000-000000000001"))
}
