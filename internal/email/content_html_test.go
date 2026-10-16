package email

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeEmailContentHTMLPreservesAuthoredNewlinesOnly(t *testing.T) {
	source := "<p title=\"attribute\nvalue\">First\r\n<strong>styled\ntext</strong><br><a href=\"https://example.com\">Link\nlabel</a> &amp;lt;</p>\n<p>Second</p><pre>code\nline</pre><style>p {\ncolor:red}</style>"
	want := "<p title=\"attribute\nvalue\">First<br><strong>styled<br>text</strong><br><a href=\"https://example.com\">Link<br>label</a> &amp;lt;</p>\n<p>Second</p><pre>code\nline</pre><style>p {\ncolor:red}</style>"
	got := NormalizeEmailContentHTML(source)
	require.Equal(t, want, got)
	require.Equal(t, got, NormalizeEmailContentHTML(got))
}
