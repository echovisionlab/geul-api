package email

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStripHTML(t *testing.T) {
	for _, test := range []struct{ name, html, text string }{
		{"paragraphs and breaks", `<p>First<br>second<br/>third</p><p>Next paragraph</p>`, "First\nsecond\nthird\n\nNext paragraph"},
		{"document metadata", `<!doctype html><html><head><title>Subject</title><meta name="description" content="hidden"><style>p {color:red}</style></head><body><p>Body<script>alert(1)</script><style>hidden</style></p></body></html>`, "Body"},
		{"links and mailto", `<p><a href="https://example.com/?a=1&amp;b=2">Read more</a> <a href="https://same.test">https://same.test</a></p><a href="mailto:help@example.com?subject=Help">Contact</a> <a href="mailto:me@example.com">me@example.com</a>`, "Read more (https://example.com/?a=1&b=2) https://same.test\n\nContact (help@example.com) me@example.com"},
		{"images and entities", `<p><img alt="Brand &amp; Co"><img alt=""><img src="tracking.gif"> &copy; &#x1F600; &nbsp; A &amp;lt;B&amp;gt;</p>`, "Brand & Co © 😀 A &lt;B&gt;"},
		{"spacing", "<p>  First   line\nsecond\t line </p>\n  <p><br><br><br> Last </p>", "First line\nsecond line\n\nLast"},
		{"layout blocks", `<header>Header</header><section><div>One</div><div>Two</div><table><tr><td>A</td><td>B</td></tr><tr><td>C</td><td>D</td></tr></table><ul><li>First</li><li>Second</li></ul></section><hr><footer>Footer</footer>`, "Header\n\nOne\nTwo\n\nA B\nC D\n\nFirst\nSecond\n\nFooter"},
		{"empty", "", ""},
		{"plain and escaped text", `Less &lt; than &amp; more`, "Less < than & more"},
		{"mailto URL label", `<a href="mailto:me@example.com">mailto:me@example.com</a>`, "me@example.com"},
		{"unquoted href", `<a href=https://example.com>Go</a>`, "Go (https://example.com)"},
	} {
		t.Run(test.name, func(t *testing.T) { require.Equal(t, test.text, StripHTML(test.html)) })
	}
}

func TestStripHTMLDropsInlineSeparatorLines(t *testing.T) {
	got := StripHTML(`<p><a href="https://example.test/privacy">Privacy Policy</a>
  &nbsp;·&nbsp;
  <a href="https://example.test/terms">Terms</a></p>`)
	want := "Privacy Policy (https://example.test/privacy)\nTerms (https://example.test/terms)"
	if got != want {
		t.Fatalf("StripHTML() = %q, want %q", got, want)
	}
}
