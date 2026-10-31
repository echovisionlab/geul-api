package emailauthoring

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/emailcontent"
	errs "github.com/echovisionlab/geul-api/internal/errors"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

func TestNormalizeEmailTemplateDocumentLocaleRequiresExactCanonicalValue(t *testing.T) {
	t.Parallel()
	locale, err := emailcontent.NormalizeLocale("pt-BR")
	require.NoError(t, err)
	require.Equal(t, "pt-BR", locale)
	for _, input := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "pt"} {
		_, err := emailcontent.NormalizeLocale(input)
		require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err), input)
	}
}
