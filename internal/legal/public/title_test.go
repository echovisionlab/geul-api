package public

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/stretchr/testify/require"
)

func TestResolveCanonicalLegalPublicTitleUsesDatabaseTranslation(t *testing.T) {
	t.Parallel()

	title := "개인정보처리방침"
	got := resolveCanonicalLegalPublicTitle("Fallback", &publiccontent.Selection{
		RequestedLocale: " ",
		DisplayedLocale: "",
		SourceLocale:    "ko",
		Title:           &title,
	})

	require.Equal(t, "개인정보처리방침", got)
}
