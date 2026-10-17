package emailauthoring

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
)

func TestTemplateListResponsePreservesSourceLocaleWithoutLoadingDocument(t *testing.T) {
	response := toProtoEmailTemplate(&model.EmailTemplate{SourceLocale: "zh-TW"})
	require.Equal(t, "zh-TW", response.SourceLocale)
	require.Nil(t, response.Document)
}
