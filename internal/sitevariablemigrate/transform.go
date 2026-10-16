package sitevariablemigrate

import (
	"context"
	"encoding/json"
	"html"
	"log/slog"
	"maps"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	xhtml "golang.org/x/net/html"
)

type occurrence struct {
	start, end int
	key        string
}

func serviceURLs(source string) string {
	return regexp.MustCompile(`https://[^\s<>"']+`).ReplaceAllStringFunc(source, func(raw string) string {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host != "opendiscogs.dsub.io" || parsed.User != nil {
			if err != nil {
				slog.WarnContext(context.Background(), "Skipping invalid service URL", "domain", "sitevariablemigrate", "operation", "service_urls", failurecause.Attr(err))
			}
			return raw
		}
		return strings.Replace(raw, "https://opendiscogs.dsub.io", "https://opendiscogs.rawsector.com", 1)
	})
}

var candidates = regexp.MustCompile(`legal@dsub\.io|privacy@dsub\.io|support@dsub\.io|DSUB\.IO|DSUB`)
var protected = regexp.MustCompile(`(?i)(?:[a-z][a-z0-9+.-]*://|www\.|(?:[a-z0-9-]+\.)+[a-z]{2,}/|/)[^\s<>"']+|[\pL\pN_.+%-]+@[\pL\pN](?:[\pL\pN.-]*[\pL\pN])?`)

func occurrences(source string) []occurrence {
	ranges := protected.FindAllStringIndex(source, -1)
	var found []occurrence
	for _, loc := range candidates.FindAllStringIndex(source, -1) {
		literal := source[loc[0]:loc[1]]
		email := strings.Contains(literal, "@")
		blocked := false
		for _, r := range ranges {
			if loc[0] < r[1] && loc[1] > r[0] && !(email && loc[0] == r[0] && loc[1] == r[1]) {
				blocked = true
			}
		}
		word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || strings.ContainsRune("_@", r) }
		if loc[0] > 0 {
			r, _ := utf8.DecodeLastRuneInString(source[:loc[0]])
			blocked = blocked || word(r)
		}
		if loc[1] < len(source) {
			r, _ := utf8.DecodeRuneInString(source[loc[1]:])
			blocked = blocked || word(r)
		}
		if blocked {
			continue
		}
		key := "site.title"
		if email {
			key = "contact." + strings.Split(literal, "@")[0] + "Email"
		}
		found = append(found, occurrence{loc[0], loc[1], key})
	}
	return found
}

// Text preserves punctuation and prevents brand substitutions inside URLs/emails.
func Text(source string) string {
	source = serviceURLs(source)
	matches := occurrences(source)
	for i := len(matches) - 1; i >= 0; i-- {
		m := matches[i]
		source = source[:m.start] + "{{" + m.key + "}}" + source[m.end:]
	}
	return source
}

func sitePath(value string) (string, bool) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || (parsed.Host != "www.dsub.io" && parsed.Host != "dsub.io") {
		// MenuJSON also passes ordinary labels here. They are not URL
		// migration failures, and a parse error would echo their full text.
		if err != nil && strings.HasPrefix(value, "https://") {
			slog.WarnContext(context.Background(), "Skipping invalid site URL", "domain", "sitevariablemigrate", "operation", "site_path", failurecause.Attr(err))
		}
		return value, false
	}
	// An absolute site's path must never turn into a protocol-relative URL.
	path := "/" + strings.TrimLeft(parsed.EscapedPath(), "/")
	if parsed.ForceQuery || parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}
	if parsed.Fragment != "" {
		path += "#" + parsed.EscapedFragment()
	}
	return path, true
}

var emailHTMLAttribute = regexp.MustCompile(`(?i)(\s+([^\s/>=]+)\s*=\s*)(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
var emailAddress = regexp.MustCompile(`[\pL\pN_.+%-]+@[\pL\pN](?:[\pL\pN.-]*[\pL\pN])?`)

func emailHrefTarget(value string) string {
	for _, name := range []string{"legal", "privacy", "support"} {
		if strings.EqualFold(value, "mailto:"+name+"@dsub.io") {
			return "mailto:{{contact." + name + "Email}}"
		}
	}
	if strings.HasPrefix(strings.ToLower(value), "mailto:") {
		// V1 only admits a token as the entire mailto address. Preserve query
		// parameters and recipient lists as literals, updating legacy addresses.
		return emailAddress.ReplaceAllStringFunc(value, func(address string) string {
			for _, name := range []string{"legal", "privacy", "support"} {
				if strings.EqualFold(address, name+"@dsub.io") {
					return name + "@rawsector.com"
				}
			}
			return address
		})
	}
	if path, ok := sitePath(value); ok {
		return "{{site.url}}" + path
	}
	return serviceURLs(value)
}

func emailText(source string) string {
	return regexp.MustCompile(`https://[^\s<>"']+`).ReplaceAllStringFunc(Text(source), func(s string) string {
		if path, ok := sitePath(s); ok {
			return "{{site.url}}" + path
		}
		return s
	})
}

// EmailSource transforms text and href attributes independently, preserving raw
// markup (including private layout markers and template placeholders).
func EmailSource(source string) string {
	tokenizer := xhtml.NewTokenizer(strings.NewReader(source))
	var result strings.Builder
	for {
		kind := tokenizer.Next()
		raw := string(tokenizer.Raw())
		switch kind {
		case xhtml.TextToken:
			raw = emailText(raw)
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			raw = emailHTMLAttribute.ReplaceAllStringFunc(raw, func(attribute string) string {
				match := emailHTMLAttribute.FindStringSubmatch(attribute)
				if !strings.EqualFold(match[2], "href") {
					return attribute
				}
				value := html.UnescapeString(match[3] + match[4] + match[5])
				next := emailHrefTarget(value)
				if next == value {
					return attribute
				}
				return match[1] + `"` + html.EscapeString(next) + `"`
			})
		}
		result.WriteString(raw)
		if kind == xhtml.ErrorToken {
			return result.String()
		}
	}
}

func atom(key string, linked bool, styles any) map[string]any {
	variable := map[string]any{"key": key}
	if linked {
		variable["linked"] = true
	}
	if styles != nil {
		variable["styles"] = styles
	}
	return map[string]any{"siteVariable": variable}
}

// rewriteTextRuns decides boundaries in the joined label/paragraph, then maps
// replacements back to runs. A match spanning styles inherits its first run's
// styles; text outside the match retains each original run's styles.
func rewriteTextRuns(runs []map[string]any, atoms bool) []any {
	var joined strings.Builder
	for _, run := range runs {
		source, _ := run["text"].(string)
		run["text"] = serviceURLs(source)
		joined.WriteString(run["text"].(string))
	}
	matches := occurrences(joined.String())
	result := make([]any, 0, len(runs))
	offset, matchIndex := 0, 0
	for _, run := range runs {
		source := run["text"].(string)
		end := offset + len(source)
		cursor := offset
		var label strings.Builder
		appendText := func(s string) {
			if !atoms {
				label.WriteString(s)
			} else if s != "" || source == "" {
				fragment := maps.Clone(run)
				fragment["text"] = s
				result = append(result, map[string]any{"text": fragment})
			}
		}
		for matchIndex < len(matches) && matches[matchIndex].start < end {
			m := matches[matchIndex]
			if m.start >= offset {
				appendText(source[cursor-offset : m.start-offset])
				if atoms {
					result = append(result, atom(m.key, false, run["styles"]))
				} else {
					label.WriteString("{{" + m.key + "}}")
				}
			}
			cursor = min(m.end, end)
			if m.end > end {
				break
			}
			matchIndex++
		}
		appendText(source[cursor-offset:])
		if !atoms && (label.Len() > 0 || source == "") {
			fragment := maps.Clone(run)
			fragment["text"] = label.String()
			result = append(result, fragment)
		}
		offset = end
	}
	return result
}

func richNode(value any) any {
	switch v := value.(type) {
	case []any:
		result := make([]any, 0, len(v))
		for i := 0; i < len(v); i++ {
			item := v[i]
			node, ok := item.(map[string]any)
			if ok {
				if text, ok := node["text"].(map[string]any); ok {
					runs := []map[string]any{text}
					for i+1 < len(v) {
						next, ok := v[i+1].(map[string]any)
						if !ok {
							break
						}
						run, ok := next["text"].(map[string]any)
						if !ok {
							break
						}
						runs = append(runs, run)
						i++
					}
					result = append(result, rewriteTextRuns(runs, true)...)
					continue
				}
				if link, ok := node["link"].(map[string]any); ok {
					href, _ := link["href"].(string)
					for _, name := range []string{"legal", "privacy", "support"} {
						if strings.EqualFold(href, "mailto:"+name+"@dsub.io") {
							content, _ := link["content"].([]any)
							var label strings.Builder
							var styles any
							for _, run := range content {
								if text, ok := run.(map[string]any); ok {
									s, _ := text["text"].(string)
									if strings.TrimSpace(label.String()) == "" && strings.TrimSpace(s) != "" {
										styles = text["styles"]
									}
									label.WriteString(s)
								}
							}
							if strings.EqualFold(strings.TrimSpace(label.String()), name+"@dsub.io") {
								result = append(result, atom("contact."+name+"Email", true, styles))
								goto next
							}
							link["href"] = "mailto:{{contact." + name + "Email}}"
							href = link["href"].(string)
						}
					}
					if path, ok := sitePath(href); ok {
						link["href"] = path
						// RichTextLink content is styled text; variable labels are represented
						// by its canonical token, resolved at materialization boundaries.
						if content, ok := link["content"].([]any); ok {
							for _, run := range content {
								if text, ok := run.(map[string]any); ok && text["text"] == href {
									text["text"] = "{{site.url}}" + path
								}
							}
						}
					} else {
						link["href"] = serviceURLs(href)
					}
					if content, ok := link["content"].([]any); ok {
						var runs []map[string]any
						for _, run := range content {
							if text, ok := run.(map[string]any); ok {
								runs = append(runs, text)
							}
						}
						link["content"] = rewriteTextRuns(runs, false)
					}
					result = append(result, node)
					continue
				}
			}
			result = append(result, richNode(item))
		next:
		}
		return result
	case map[string]any:
		for key, item := range v {
			v[key] = richNode(item)
		}
	}
	return value
}

func RichJSON(source []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(source, &value); err != nil {
		return nil, err
	}
	value = richNode(value)
	return json.Marshal(value)
}

func MenuJSON(source []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(source, &value); err != nil {
		return nil, err
	}
	var walk func(any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case string:
			if path, ok := sitePath(t); ok {
				return path
			}
		case []any:
			for i, x := range t {
				t[i] = walk(x)
			}
		case map[string]any:
			for k, x := range t {
				t[k] = walk(x)
			}
		}
		return v
	}
	return json.Marshal(walk(value))
}
