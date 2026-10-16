package llm

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderFailureDetailsAreBounded(t *testing.T) {
	err := NewProviderFailure(ProviderFailureDetails{
		Category:        ProviderFailureCategory("provider supplied category"),
		HTTPStatusClass: "418",
		ExceptionType:   ProviderExceptionType("provider supplied exception"),
	}, errors.New("provider response body: secret"))

	details, ok := ProviderFailureDetailsFromError(err)
	require.True(t, ok)
	require.Equal(t, ProviderFailureInternal, details.Category)
	require.Empty(t, details.HTTPStatusClass)
	require.Equal(t, ProviderExceptionUnknown, details.ExceptionType)
	require.NotContains(t, err.Error(), "secret")
}
