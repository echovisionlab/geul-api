package dberrors

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

type sqlStateError string

func (err sqlStateError) Error() string    { return "sqlstate " + string(err) }
func (err sqlStateError) SQLState() string { return string(err) }

func TestRetrySerializationFailureRetriesOnlySerializationFailures(t *testing.T) {
	serialization := fmt.Errorf("load page: %w", sqlStateError("40001"))
	for _, tc := range []struct {
		name     string
		failures []error
		wantErr  error
		wantRuns int
	}{
		{name: "succeeds after a conflict", failures: []error{serialization}, wantRuns: 2},
		{name: "gives up after the bounded attempts", failures: []error{serialization, serialization, serialization, serialization}, wantErr: serialization, wantRuns: SerializationRetryAttempts},
		{name: "returns other failures at once", failures: []error{sqlStateError("23505")}, wantErr: sqlStateError("23505"), wantRuns: 1},
		{name: "runs once on success", wantRuns: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runs := 0
			err := RetrySerializationFailure(context.Background(), func() error {
				runs++
				if runs <= len(tc.failures) {
					return tc.failures[runs-1]
				}
				return nil
			})
			require.Equal(t, tc.wantRuns, runs)
			if tc.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.True(t, errors.Is(err, tc.wantErr) || err == tc.wantErr)
			}
		})
	}
}

func TestRetrySerializationFailureStopsWhenTheRequestEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runs := 0
	err := RetrySerializationFailure(ctx, func() error {
		runs++
		cancel()
		return sqlStateError("40001")
	})
	require.Error(t, err)
	require.Equal(t, 1, runs)
}
