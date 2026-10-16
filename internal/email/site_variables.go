package email

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

var emailHref = regexp.MustCompile(`(?i)\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)

// ValidateSiteVariableHrefs admits only the documented reserved token targets.
func ValidateSiteVariableHrefs(source string) error {
	for _, match := range emailHref.FindAllStringSubmatch(source, -1) {
		value := html.UnescapeString(match[1] + match[2] + match[3])
		if !strings.Contains(strings.ToLower(value), "{{site.") && !strings.Contains(strings.ToLower(value), "{{contact.") {
			continue
		}
		valid := value == "{{site.url}}" || strings.HasPrefix(value, "{{site.url}}/")
		for _, key := range []string{"legalEmail", "email"} {
			valid = valid || value == "mailto:{{contact."+key+"}}"
		}
		if !valid {
			return fmt.Errorf("invalid site variable href %q", value)
		}
		probe := strings.ReplaceAll(value, "{{site.url}}", "https://site.invalid")
		for _, key := range []string{"legalEmail", "email"} {
			probe = strings.ReplaceAll(probe, "{{contact."+key+"}}", "contact@site.invalid")
		}
		if strings.Contains(probe, "{{") || !safeEmailHref(probe) {
			return fmt.Errorf("invalid site variable href %q", value)
		}
	}
	return nil
}

func safeEmailHref(value string) bool {
	if strings.Contains(value, "\\") || strings.IndexFunc(value, unicode.IsControl) >= 0 || strings.HasPrefix(value, "//") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	if strings.HasPrefix(value, "/") {
		return true
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https", "http":
		return parsed.Host != "" && parsed.User == nil
	case "mailto", "tel":
		decoded, err := url.PathUnescape(value)
		return err == nil && strings.IndexFunc(decoded, unicode.IsControl) < 0
	}
	return strings.HasPrefix(value, "#") || value == ""
}

func resolveEmailHrefs(source string, data map[string]string) (string, error) {
	var firstErr error
	source = emailHref.ReplaceAllStringFunc(source, func(attribute string) string {
		match := emailHref.FindStringSubmatch(attribute)
		value := html.UnescapeString(match[1] + match[2] + match[3])
		if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
			origin := data["site.url"]
			if origin == "" {
				origin = data["site_origin"]
			}
			if origin == "" {
				firstErr = fmt.Errorf("site.url is required for relative email hrefs")
			} else {
				value = strings.TrimRight(origin, "/") + value
			}
		}
		if !safeEmailHref(value) {
			firstErr = fmt.Errorf("unsafe rendered email href %q", value)
		}
		return `href="` + html.EscapeString(value) + `"`
	})
	return source, firstErr
}
