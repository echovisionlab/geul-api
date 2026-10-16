package email

import (
	"html"
	"strings"

	xhtml "golang.org/x/net/html"
)

// NormalizeEmailContentHTML also handles persisted projections created before
// the Block renderer serialized authored text newlines as explicit breaks.
// Apply to rich-text content before wrapping it in a raw HTML layout: layout
// source indentation, CSS, attributes and preformatted code are not authored
// rich-text line breaks.
func NormalizeEmailContentHTML(source string) string {
	if !strings.ContainsAny(source, "\r\n") {
		return source
	}
	z := xhtml.NewTokenizer(strings.NewReader(source))
	var out strings.Builder
	excluded := ""
	for {
		kind := z.Next()
		if kind == xhtml.ErrorToken {
			return out.String()
		}
		if kind == xhtml.StartTagToken || kind == xhtml.EndTagToken {
			tag, _ := z.TagName()
			name := string(tag)
			if kind == xhtml.EndTagToken && name == excluded {
				excluded = ""
			} else if kind == xhtml.StartTagToken && excluded == "" {
				switch name {
				case "head", "script", "style", "pre":
					excluded = name
				}
			}
		}
		if kind == xhtml.TextToken && excluded == "" {
			value := html.UnescapeString(string(z.Raw()))
			if strings.TrimSpace(value) != "" && strings.ContainsAny(value, "\r\n") {
				value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
				out.WriteString(strings.ReplaceAll(html.EscapeString(value), "\n", "<br>"))
				continue
			}
		}
		out.Write(z.Raw())
	}
}
