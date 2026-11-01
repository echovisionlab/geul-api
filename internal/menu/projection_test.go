package menu

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMenuResponsePreservesSourceLocale(t *testing.T) {
	require.Equal(t, "pt-BR", menuMessage(Menu{SourceLocale: "pt-BR"}).SourceLocale)
}
