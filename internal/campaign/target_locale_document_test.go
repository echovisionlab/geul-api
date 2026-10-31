package campaign

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/emailcontent"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

func TestNormalizeCampaignDocumentLocaleRequiresExactCanonicalValue(t *testing.T) {
	t.Parallel()
	locale, err := emailcontent.NormalizeLocale("es-419")
	require.NoError(t, err)
	require.Equal(t, "es-419", locale)
	for _, locale := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "es-MX"} {
		_, err := emailcontent.NormalizeLocale(locale)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), locale)
	}
}
