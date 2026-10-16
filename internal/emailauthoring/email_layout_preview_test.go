package emailauthoring

import (
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/stretchr/testify/require"
)

func TestLayoutPreviewUsesDeliveryNewlinesAndEmptyParagraphs(t *testing.T) {
	source := "<html><head><title>{{subject}}</title></head><body><main>{{content}}</main><footer>Footer\nline</footer></body></html>"
	body := "<p>First<br>line</p><p></p><p>Second</p>"
	data := map[string]string{"subject": "Preview"}
	preview, err := renderLayoutPreviewHTML(source, body, data)
	require.NoError(t, err)
	canonical, err := email.CanonicalizeLayoutSourceMarkers(source)
	require.NoError(t, err)
	layout, err := email.ResolveLayoutLocaleMarkup(canonical, nil)
	require.NoError(t, err)
	delivery, err := email.RenderHTMLVarsStrict(strings.ReplaceAll(layout, "{{content}}", body), data)
	require.NoError(t, err)
	require.Equal(t, email.NormalizeRenderedHTML(delivery), preview)
	require.Contains(t, preview, "<p>First<br>line</p><p>&nbsp;</p><p>Second</p>")
	require.Contains(t, preview, "<footer>Footer<br>line</footer>")
	require.Equal(t, "First\nline\n\nSecond\n\nFooter\nline", email.StripHTML(preview))
}
