package jobresult

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResultClassification(t *testing.T) {
	t.Parallel()
	cause := errors.New("cause")

	require.NoError(t, Retry(nil))
	retry := Retry(cause)
	require.Equal(t, cause.Error(), retry.Error())
	require.ErrorIs(t, retry, cause)
	require.True(t, IsRetry(fmt.Errorf("wrapped: %w", retry)))
	require.False(t, IsTerminal(retry))

	require.NoError(t, Terminal(nil))
	terminal := Terminal(cause)
	require.Equal(t, cause.Error(), terminal.Error())
	require.ErrorIs(t, terminal, cause)
	require.True(t, IsTerminal(fmt.Errorf("wrapped: %w", terminal)))
	require.False(t, IsRetry(terminal))
}
