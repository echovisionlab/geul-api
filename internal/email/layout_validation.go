package email

import (
	"fmt"
	"regexp"
	"strings"
)

type LayoutValidationIssue struct {
	Code    string
	Message string
}

var (
	layoutDocTypeRe  = regexp.MustCompile(`(?is)<!doctype\s+html\b[^>]*>`)
	layoutOpenTagRe  = regexp.MustCompile(`(?is)<\s*([a-z][a-z0-9:-]*)(?:\s|>|/)`)
	layoutCloseTagRe = regexp.MustCompile(`(?is)</\s*([a-z][a-z0-9:-]*)\s*>`)
)

// ValidateLayoutHTMLContent checks email layout HTML before it can be used to
// wrap campaign or system email bodies. A layout is allowed to be either a full
// HTML document or a fragment, but it must define exactly one body insertion
// point and must not concatenate multiple full documents.
func ValidateLayoutHTMLContent(content string) []LayoutValidationIssue {
	content = NormalizeTemplatePlaceholders(content)
	if strings.TrimSpace(content) == "" {
		return []LayoutValidationIssue{{
			Code:    "EMPTY_CONTENT",
			Message: "HTML content is empty",
		}}
	}

	var issues []LayoutValidationIssue
	contentPlaceholderCount := strings.Count(content, "{{content}}")
	switch {
	case contentPlaceholderCount == 0:
		issues = append(issues, LayoutValidationIssue{
			Code:    "MISSING_CONTENT_PLACEHOLDER",
			Message: "HTML must contain {{content}} placeholder where email body will be inserted",
		})
	case contentPlaceholderCount > 1:
		issues = append(issues, LayoutValidationIssue{
			Code:    "MULTIPLE_CONTENT_PLACEHOLDERS",
			Message: "HTML must contain exactly one {{content}} placeholder",
		})
	}

	docTypeCount := len(layoutDocTypeRe.FindAllStringIndex(content, -1))
	if docTypeCount > 1 {
		issues = append(issues, LayoutValidationIssue{
			Code:    "MULTIPLE_HTML_DOCUMENTS",
			Message: fmt.Sprintf("HTML must contain at most one document doctype; found %d", docTypeCount),
		})
	}

	openCounts := countLayoutOpenTags(content)
	closeCounts := countLayoutCloseTags(content)
	for _, tag := range []string{"html", "head", "body", "table", "div", "style"} {
		openCount := openCounts[tag]
		closeCount := closeCounts[tag]
		if openCount != closeCount {
			issues = append(issues, LayoutValidationIssue{
				Code:    "UNCLOSED_TAG",
				Message: fmt.Sprintf("Unclosed <%s> tag: found %d opening and %d closing tags", tag, openCount, closeCount),
			})
		}
	}
	for _, tag := range []string{"html", "head", "body"} {
		openCount := openCounts[tag]
		if openCount > 1 {
			issues = append(issues, LayoutValidationIssue{
				Code:    "MULTIPLE_HTML_DOCUMENTS",
				Message: fmt.Sprintf("HTML must contain at most one <%s> document tag; found %d", tag, openCount),
			})
		}
	}

	return issues
}

func ValidateLayoutHTMLContentError(content string) error {
	issues := ValidateLayoutHTMLContent(content)
	if len(issues) == 0 {
		return nil
	}
	return fmt.Errorf("%s: %s", issues[0].Code, issues[0].Message)
}

func countLayoutOpenTags(content string) map[string]int {
	counts := map[string]int{}
	for _, match := range layoutOpenTagRe.FindAllStringSubmatch(content, -1) {
		if len(match) < 2 {
			continue
		}
		tag := strings.ToLower(match[1])
		if tag == "meta" || tag == "link" || tag == "img" || tag == "br" || tag == "hr" || tag == "input" {
			continue
		}
		counts[tag]++
	}
	return counts
}

func countLayoutCloseTags(content string) map[string]int {
	counts := map[string]int{}
	for _, match := range layoutCloseTagRe.FindAllStringSubmatch(content, -1) {
		if len(match) < 2 {
			continue
		}
		counts[strings.ToLower(match[1])]++
	}
	return counts
}
