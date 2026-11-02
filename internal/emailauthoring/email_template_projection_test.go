package emailauthoring

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTemplateListResponsePreservesSourceLocaleWithoutLoadingDocument(t *testing.T) {
	response := emailTemplateMessage(Template{SourceLocale: "zh-TW"})
	require.Equal(t, "zh-TW", response.SourceLocale)
	require.Nil(t, response.Document)
}
