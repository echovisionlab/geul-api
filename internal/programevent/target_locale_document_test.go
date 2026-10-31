package programevent

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

func TestNormalizeProgramEventDocumentLocaleRequiresExactCanonicalValue(t *testing.T) {
	t.Parallel()
	locale, err := normalizeProgramEventDocumentLocale("zh-CN")
	require.NoError(t, err)
	require.Equal(t, "zh-CN", locale)
	for _, input := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "es-MX"} {
		_, err := normalizeProgramEventDocumentLocale(input)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), input)
	}
}
