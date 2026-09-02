package application

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigJSONSemanticallyEqualIgnoresObjectKeyOrder(t *testing.T) {
	require.True(t, configJSONSemanticallyEqual(
		[]byte(`{"api_key":"secret","model":"model"}`),
		[]byte(`{"model":"model","api_key":"secret"}`),
	))
}
