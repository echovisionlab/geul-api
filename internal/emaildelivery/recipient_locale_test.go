package emaildelivery

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveEmailRecipientLocaleNormalizesUserLocale(t *testing.T) {
	require.Equal(t, "pt-BR", resolveEmailRecipientLocale("pt"))
	require.Empty(t, resolveEmailRecipientLocale("unsupported"))
}
