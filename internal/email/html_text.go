package email

import (
	"html"
	"strings"

	xhtml "golang.org/x/net/html"
)

// StripHTML converts email HTML to its readable plain-text alternative.
// Keep structural breaks and link destinations without leaking head/style text.
func StripHTML(source string) string {
	z := xhtml.NewTokenizer(strings.NewReader(source))
	var out strings.Builder
	hidden := ""
	linkHref, originalHref, linkStart := "", "", 0
	boundary := func(count int) {
		text := out.String()
		newlines := 0
		for i := len(text) - 1; i >= 0; i-- {
			switch text[i] {
			case '\n':
				newlines++
			case ' ', '\t', '\r':
			default:
				i = -1
			}
		}
		if newlines < count {
			out.WriteString(strings.Repeat("\n", count-newlines))
		}
	}
	for {
		kind := z.Next()
		if kind == xhtml.ErrorToken {
			break
		}
		if kind == xhtml.TextToken {
			if hidden == "" {
				value := html.UnescapeString(string(z.Raw()))
				if strings.TrimSpace(value) == "" {
					value = " " // Ignore source formatting between elements.
				}
				out.WriteString(value)
			}
			continue
		}
		if kind != xhtml.StartTagToken && kind != xhtml.EndTagToken && kind != xhtml.SelfClosingTagToken {
			continue
		}
		token := z.Token()
		tag := token.Data
		if hidden != "" {
			if kind == xhtml.EndTagToken && tag == hidden {
				hidden = ""
			} else if hidden == "head" && tag == "body" {
				hidden = ""
			} else {
				continue
			}
		}
		if tag == "head" || tag == "script" || tag == "style" || tag == "title" {
			if kind == xhtml.StartTagToken {
				hidden = tag
			}
			continue
		}
		attribute := func(key string) string {
			for _, attr := range token.Attr {
				if attr.Key == key {
					return strings.TrimSpace(attr.Val)
				}
			}
			return ""
		}
		if kind != xhtml.EndTagToken {
			switch tag {
			case "br":
				out.WriteByte('\n')
			case "img":
				out.WriteString(attribute("alt"))
			case "a":
				linkHref, linkStart = attribute("href"), out.Len()
				originalHref = linkHref
				if strings.HasPrefix(strings.ToLower(linkHref), "mailto:") {
					linkHref = strings.SplitN(linkHref[len("mailto:"):], "?", 2)[0]
				}
			}
		} else if tag == "a" && linkHref != "" {
			label := strings.Join(strings.Fields(out.String()[linkStart:]), " ")
			if label == "" {
				out.WriteString(linkHref)
			} else if label == originalHref && originalHref != linkHref {
				prefix := out.String()[:linkStart]
				out.Reset()
				out.WriteString(prefix)
				out.WriteString(linkHref)
			} else if label != linkHref {
				out.WriteString(" (" + linkHref + ")")
			}
			linkHref = ""
		}
		// Boundaries on both sides also separate blocks nested in inline text.
		switch tag {
		case "p", "h1", "h2", "h3", "h4", "h5", "h6", "table", "section", "header", "footer", "hr", "blockquote", "pre", "article", "main", "ul", "ol":
			boundary(2)
		case "div", "tr", "li":
			boundary(1)
		case "td", "th":
			out.WriteByte(' ')
		}
	}

	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(out.String(), "\r\n", "\n"), "\r", "\n"), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if isSeparatorOnly(line) {
			continue // Inline footer separators (·, |) carry no meaning in plain text.
		}
		if line == "" && (len(result) == 0 || result[len(result)-1] == "") {
			continue
		}
		result = append(result, line)
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

func isSeparatorOnly(line string) bool {
	if line == "" {
		return false
	}
	for _, r := range line {
		switch r {
		case '·', '•', '|', '-', '–', '—', ' ':
		default:
			return false
		}
	}
	return true
}
