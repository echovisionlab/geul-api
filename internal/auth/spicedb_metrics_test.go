package auth

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSpiceDBWriteOutcome(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "succeeded", want: spiceDBOutcomeSucceeded},
		{name: "definite failure", err: errors.New("definite"), want: spiceDBOutcomeFailed},
		{name: "uncertain after retries", err: &relationshipWriteOutcomeUncertainError{err: errors.New("ambiguous")}, want: spiceDBOutcomeUncertain},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, spiceDBWriteOutcome(test.err))
		})
	}
}
