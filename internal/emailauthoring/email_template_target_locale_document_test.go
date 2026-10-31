package emailauthoring

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

func TestNormalizeEmailTemplateDocumentLocaleRequiresExactCanonicalValue(t *testing.T) {
	t.Parallel()
	locale, err := normalizeEmailTemplateDocumentLocale("pt-BR")
	require.NoError(t, err)
	require.Equal(t, "pt-BR", locale)
	for _, input := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "pt"} {
		_, err := normalizeEmailTemplateDocumentLocale(input)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), input)
	}
}
