package release

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReleaseProtoPreservesSourceLocale(t *testing.T) {
	for _, locale := range []string{"en", "ko", "ja"} {
		t.Run(locale, func(t *testing.T) {
			root := Release{ID: "11111111-1111-4111-8111-111111111111", SourceLocale: locale}
			require.Equal(t, locale, ReleaseMessage(View{Release: root}).SourceLocale, "Create/Get/List share the native Release projection")
		})
	}
}
