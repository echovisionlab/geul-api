package email

import (
	"html"
	"strings"

	xhtml "golang.org/x/net/html"
)

// The existing document editor's light quote baseline uses a 2px border,
// 0.75rem inset, muted text, and 1.7 line height. Physical px properties work in
// mail clients without the editor's CSS variables or logical-property support.
const emailQuoteDefaults = "margin:0;padding-left:12px;border-left:2px solid #bbb7aa;color:#5c594f;font-size:16px;line-height:1.7;overflow-wrap:anywhere;"

// NormalizeEmailContentHTML also handles persisted projections created before
// the Block renderer serialized authored text newlines as explicit breaks.
// Apply to rich-text content before wrapping it in a raw HTML layout: layout
// source indentation, CSS, attributes and preformatted code are not authored
// rich-text line breaks.
func NormalizeEmailContentHTML(source string) string {
	if !strings.ContainsAny(source, "\r\n") && !strings.Contains(strings.ToLower(source), "<blockquote") {
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
		// Token decoding may unescape attributes in the tokenizer buffer. Keep
		// every untouched tag byte-stable before reading its decoded token.
		raw := string(z.Raw())
		if kind == xhtml.StartTagToken || kind == xhtml.EndTagToken || kind == xhtml.SelfClosingTagToken {
			token := z.Token()
			name := token.Data
			if name == "blockquote" && excluded == "" && kind != xhtml.EndTagToken {
				out.WriteString(emailQuoteTag(token))
				continue
			}
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
			value := html.UnescapeString(raw)
			if strings.TrimSpace(value) != "" && strings.ContainsAny(value, "\r\n") {
				value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
				out.WriteString(strings.ReplaceAll(html.EscapeString(value), "\n", "<br>"))
				continue
			}
		}
		out.WriteString(raw)
	}
}

func emailQuoteTag(token xhtml.Token) string {
	for index := range token.Attr {
		attribute := &token.Attr[index]
		if attribute.Key == "style" {
			if !strings.HasPrefix(attribute.Val, emailQuoteDefaults) {
				// Author styles come last, including CSS shorthand declarations,
				// so an explicit border, font, color, or inset still wins.
				attribute.Val = emailQuoteDefaults + attribute.Val
			}
			return token.String()
		}
	}
	token.Attr = append(token.Attr, xhtml.Attribute{Key: "style", Val: emailQuoteDefaults})
	return token.String()
}
