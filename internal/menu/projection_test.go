package menu

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
)

func TestMenuResponsePreservesSourceLocale(t *testing.T) {
	response, err := new(MenuService).toProtoMenu(&model.Menu{SourceLocale: "pt-BR"})
	require.NoError(t, err)
	require.Equal(t, "pt-BR", response.SourceLocale)
}
